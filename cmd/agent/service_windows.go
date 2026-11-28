//go:build windows

package main

// The HopDesk Windows service and how it's installed. See service.go for the
// overall design.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "HopDesk"
	sasEvent    = `Global\HopDeskSAS`
	sasPolicy   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
)

var (
	sasDLL      = windows.NewLazySystemDLL("sas.dll")
	procSendSAS = sasDLL.NewProc("SendSAS")

	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

func serviceSupported() bool { return true }

// serviceDataDir: settings and identity while login-screen control is on.
func serviceDataDir() string {
	p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return ""
	}
	return filepath.Join(p, "HopDesk")
}

// serviceProgramDir: the service's own copy of HopDesk. It lives where only
// administrators can change it, because it runs as the system account.
func serviceProgramDir() string {
	p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return ""
	}
	return filepath.Join(p, "HopDesk")
}

// serviceStatus works without administrator rights.
func serviceStatus() (installed, running bool) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, false
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &st); err != nil {
		return true, false
	}
	return true, st.CurrentState == windows.SERVICE_RUNNING
}

func currentSessionID() int {
	var sid uint32
	_ = windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid)
	return int(sid)
}

// userHome is where the file browser starts and uploads go. The worker runs
// as the system account, so it uses the profile of whoever is logged in.
func userHome() string {
	if role == roleWorker {
		var tok windows.Token
		if err := windows.WTSQueryUserToken(uint32(currentSessionID()), &tok); err == nil {
			defer tok.Close()
			if dir, err := tok.GetUserProfileDirectory(); err == nil && dir != "" {
				return dir
			}
		}
		return `C:\Users\Public` // nobody is logged in yet
	}
	h, _ := os.UserHomeDir()
	return h
}

// exitWithProcess ends the worker when the service that started it is gone,
// so a restarted service never ends up with two workers.
func exitWithProcess(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		log.Printf("the HopDesk service (process %d) is gone; exiting", pid)
		os.Exit(0)
	}
	_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
	log.Printf("the HopDesk service stopped; exiting")
	os.Exit(0)
}

// ---- Ctrl+Alt+Del ----

// Only the service may press Ctrl+Alt+Del (SendSAS), so the worker asks it
// through an event that only the system account can open.

func sasAvailable() bool { return role == roleWorker }

func requestSAS() {
	name, _ := windows.UTF16PtrFromString(sasEvent)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		log.Printf("Ctrl+Alt+Del: %v", err)
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.SetEvent(h)
}

func listenForSAS() {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)")
	if err != nil {
		log.Printf("Ctrl+Alt+Del: %v", err)
		return
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, _ := windows.UTF16PtrFromString(sasEvent)
	ev, err := windows.CreateEvent(&sa, 0, 0, name)
	if err != nil {
		log.Printf("Ctrl+Alt+Del: %v", err)
		return
	}
	var last time.Time
	for {
		r, err := windows.WaitForSingleObject(ev, windows.INFINITE)
		if err != nil || r != windows.WAIT_OBJECT_0 {
			time.Sleep(time.Second)
			continue
		}
		if time.Since(last) < 2*time.Second {
			continue // one is enough
		}
		last = time.Now()
		if err := procSendSAS.Find(); err != nil {
			log.Printf("Ctrl+Alt+Del isn't available on this version of Windows")
			continue
		}
		procSendSAS.Call(0) // FALSE: called from a service
		log.Printf("sent Ctrl+Alt+Del")
	}
}

// ---- the service ----

type hopService struct{}

func (hopService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	go listenForSAS()
	var w workerSupervisor
	w.ensure()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				w.stop()
				log.Printf("service stopped")
				return false, 0
			}
		case <-tick.C:
			w.ensure()
		}
	}
}

// workerSupervisor keeps one worker running in the session that's on the
// screen (the console session), including before anyone logs in.
type workerSupervisor struct {
	proc    windows.Handle
	session uint32
	started time.Time
	next    time.Time // don't start again before this (after quick crashes)
	backoff time.Duration
}

func (w *workerSupervisor) ensure() {
	sid := windows.WTSGetActiveConsoleSessionId()
	if sid == 0xFFFFFFFF {
		return // the console is switching between sessions
	}
	if w.proc != 0 {
		ev, _ := windows.WaitForSingleObject(w.proc, 0)
		alive := ev == uint32(windows.WAIT_TIMEOUT)
		if alive && w.session == sid {
			return
		}
		if alive {
			log.Printf("the screen moved to session %d; moving the worker", sid)
			w.kill()
		} else {
			var code uint32
			_ = windows.GetExitCodeProcess(w.proc, &code)
			log.Printf("worker exited (code %d)", code)
			_ = windows.CloseHandle(w.proc)
			w.proc = 0
			if code != 0 && time.Since(w.started) < 10*time.Second {
				w.backoff = min(max(2*w.backoff, 2*time.Second), time.Minute)
			} else {
				w.backoff = 0 // normal exit (settings changed) or it ran fine for a while
			}
			w.next = time.Now().Add(w.backoff)
		}
	}
	if time.Now().Before(w.next) {
		return
	}
	h, err := startWorker(sid)
	if err != nil {
		logOnce("worker:"+err.Error(), "can't start the worker in session %d: %v", sid, err)
		w.next = time.Now().Add(10 * time.Second)
		return
	}
	w.proc, w.session, w.started = h, sid, time.Now()
	log.Printf("worker started in session %d", sid)
}

func (w *workerSupervisor) kill() {
	if w.proc == 0 {
		return
	}
	_ = windows.TerminateProcess(w.proc, 0)
	_, _ = windows.WaitForSingleObject(w.proc, 5000)
	_ = windows.CloseHandle(w.proc)
	w.proc = 0
}

func (w *workerSupervisor) stop() { w.kill() }

// startWorker starts "HopDesk.exe -worker" as the system account in the
// given session, on its interactive desktop.
func startWorker(session uint32) (windows.Handle, error) {
	var self windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_SESSIONID|windows.TOKEN_ADJUST_DEFAULT, &self)
	if err != nil {
		return 0, fmt.Errorf("open token: %w", err)
	}
	defer self.Close()
	var tok windows.Token
	if err := windows.DuplicateTokenEx(self, windows.MAXIMUM_ALLOWED, nil, windows.SecurityIdentification, windows.TokenPrimary, &tok); err != nil {
		return 0, fmt.Errorf("duplicate token: %w", err)
	}
	defer tok.Close()
	if err := windows.SetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4); err != nil {
		return 0, fmt.Errorf("set session: %w", err)
	}
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		return 0, fmt.Errorf("environment: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)

	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmdline, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{exe, "-worker", "-parent", strconv.Itoa(os.Getpid())}))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	err = windows.CreateProcessAsUser(tok, nil, cmdline, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, dir, &si, &pi)
	if err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

// ---- command lines: -service, -service-install, -service-uninstall ----

func runServiceCommand(args []string) int {
	opts := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 {
		opts[strings.TrimPrefix(args[i], "-")] = args[i+1]
	}
	switch args[0] {
	case "-service":
		serviceLogging()
		log.Printf("HopDesk service %s starting", version)
		if err := svc.Run(serviceName, hopService{}); err != nil {
			log.Printf("service: %v", err)
			return 1
		}
		return 0
	case "-service-install", "-service-uninstall":
		if !windows.GetCurrentProcessToken().IsElevated() {
			// Started without administrator rights (for example by the
			// uninstaller): ask Windows, and run again elevated.
			exe, err := os.Executable()
			if err == nil {
				err = runElevated(exe, args, 5*time.Minute)
			}
			if err != nil {
				return finish(opts["result"], err)
			}
			return 0 // the elevated copy wrote the result
		}
		if args[0] == "-service-install" {
			return finish(opts["result"], installService(opts["from"], opts["sid"]))
		}
		return finish(opts["result"], uninstallService(opts["to"]))
	}
	return 2
}

// finish reports the outcome of an elevated install or uninstall to the app
// that asked for it (it can't see this process's output).
func finish(resultFile string, err error) int {
	msg, code := "ok", 0
	if err != nil {
		msg, code = err.Error(), 1
		fmt.Fprintln(os.Stderr, err)
	}
	if resultFile != "" {
		_ = os.WriteFile(resultFile, []byte(msg), 0o644)
	}
	return code
}

func serviceLogging() {
	dir := serviceDataDir()
	_ = os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "service.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(p, p+".old")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(f)
	}
}

// setServiceEnabled asks Windows for administrator permission (the UAC
// prompt) and installs or removes the service in that elevated process.
func setServiceEnabled(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	res, err := os.CreateTemp("", "hopdesk-service-*.txt")
	if err != nil {
		return err
	}
	resPath := res.Name()
	res.Close()
	defer os.Remove(resPath)

	args := []string{"-service-uninstall", "-to", userDir(), "-result", resPath}
	if on {
		tu, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return err
		}
		// The user's own folder and account: the elevated process may run as
		// a different administrator account.
		args = []string{"-service-install", "-from", userDir(), "-sid", tu.User.Sid.String(), "-result", resPath}
	}
	if err := runElevated(exe, args, 5*time.Minute); err != nil {
		return err
	}
	out, _ := os.ReadFile(resPath)
	if s := strings.TrimSpace(string(out)); s != "ok" {
		if s == "" {
			s = "it didn't finish"
		}
		return errors.New(s)
	}
	return nil
}

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	verb         *uint16
	file         *uint16
	parameters   *uint16
	directory    *uint16
	show         int32
	instApp      uintptr
	idList       uintptr
	class        *uint16
	keyClass     uintptr
	hotKey       uint32
	iconOrMonitr uintptr
	process      windows.Handle
}

// runElevated runs exe with args as administrator and waits for it.
func runElevated(exe string, args []string, timeout time.Duration) error {
	const seeMaskNoCloseProcess, seeMaskNoAsync = 0x40, 0x100
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, verb: verb, file: file, parameters: params}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if ok, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("Windows didn't get permission: the administrator prompt was cancelled")
		}
		return fmt.Errorf("couldn't ask for administrator permission: %v", err)
	}
	if info.process == 0 {
		return errors.New("couldn't start the installer")
	}
	defer windows.CloseHandle(info.process)
	ev, _ := windows.WaitForSingleObject(info.process, uint32(timeout/time.Millisecond))
	if ev != windows.WAIT_OBJECT_0 {
		return errors.New("timed out waiting for the installer")
	}
	return nil
}

// ---- install and uninstall (run as administrator) ----

func installService(from, sid string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("this needs administrator permission")
	}
	if from == "" {
		return errors.New("missing -from")
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return fmt.Errorf("bad account id %q", sid)
	}
	dataDir, progDir := serviceDataDir(), serviceProgramDir()
	if dataDir == "" || progDir == "" {
		return errors.New("can't find the ProgramData and Program Files folders")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	exists := err == nil
	if exists {
		defer s.Close()
		stopService(s) // so its files can be replaced
	}

	// 1. The service's own copy of HopDesk (and ffmpeg), where only administrators can change it.
	if err := os.MkdirAll(progDir, 0o755); err != nil {
		return err
	}
	if !samePath(filepath.Dir(exe), progDir) {
		if err := copyFile(exe, filepath.Join(progDir, "HopDesk.exe")); err != nil {
			return fmt.Errorf("copy HopDesk: %w", err)
		}
		for _, extra := range []string{"ffmpeg.exe", "ffmpeg-LICENSE.txt"} {
			if src := filepath.Join(filepath.Dir(exe), extra); fileExists(src) {
				if err := copyFile(src, filepath.Join(progDir, extra)); err != nil {
					return fmt.Errorf("copy %s: %w", extra, err)
				}
			}
		}
	}

	// 2. Settings and identity, readable by the service and by this user only.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := lockDown(dataDir, sid); err != nil {
		return fmt.Errorf("protect %s: %w", dataDir, err)
	}
	if !fileExists(filepath.Join(dataDir, "app.json")) {
		if !fileExists(filepath.Join(from, "app.json")) {
			return errors.New("set up HopDesk on this computer first (under Connection), then try again")
		}
		for _, f := range []string{"app.json", "agent.json"} {
			if src := filepath.Join(from, f); fileExists(src) {
				if err := copyFile(src, filepath.Join(dataDir, f)); err != nil {
					return fmt.Errorf("copy settings: %w", err)
				}
			}
		}
	}

	// 3. The service.
	bin := filepath.Join(progDir, "HopDesk.exe")
	if exists {
		c, err := s.Config()
		if err != nil {
			return err
		}
		c.BinaryPathName = windows.EscapeArg(bin) + " -service"
		c.StartType = mgr.StartAutomatic
		if err := s.UpdateConfig(c); err != nil {
			return err
		}
	} else {
		s, err = m.CreateService(serviceName, bin, mgr.Config{
			DisplayName: "HopDesk",
			Description: "Lets HopDesk show and control the login screen and admin prompts. Turn it off in HopDesk's settings.",
			StartType:   mgr.StartAutomatic,
		}, "-service")
		if err != nil {
			return fmt.Errorf("create the service: %w", err)
		}
		defer s.Close()
	}
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 2 * time.Second}
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 24*60*60)

	// 4. Let the service press Ctrl+Alt+Del (only if nobody set this policy already).
	allowSoftwareSAS(dataDir)

	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}
	return nil
}

func uninstallService(to string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("this needs administrator permission")
	}
	dataDir, progDir := serviceDataDir(), serviceProgramDir()
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		err := s.Delete()
		s.Close()
		if err != nil {
			return fmt.Errorf("remove the service: %w", err)
		}
	}
	// Bring the settings and identity back, so the app keeps the same
	// password and fingerprint.
	if to != "" && fileExists(filepath.Join(dataDir, "app.json")) {
		if err := os.MkdirAll(to, 0o700); err != nil {
			return err
		}
		for _, f := range []string{"app.json", "agent.json"} {
			if src := filepath.Join(dataDir, f); fileExists(src) {
				if err := copyFile(src, filepath.Join(to, f)); err != nil {
					return fmt.Errorf("copy settings back: %w", err)
				}
			}
		}
	}
	restoreSoftwareSAS(dataDir)
	for i := 0; i < 10; i++ { // the worker may take a moment to exit
		errA, errB := os.RemoveAll(progDir), os.RemoveAll(dataDir)
		if errA == nil && errB == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func stopService(s *mgr.Service) {
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return
	}
	_, _ = s.Control(svc.Stop)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if st, err := s.Query(); err != nil || st.State == svc.Stopped {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // let the processes release their files
}

// lockDown limits a folder to the system account, administrators, and the
// user who turned login-screen control on.
func lockDown(dir, sid string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;" + sid + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// SoftwareSASGeneration lets services press Ctrl+Alt+Del. HopDesk only sets
// it when it isn't set, and remembers that so it can undo it.
func allowSoftwareSAS(dataDir string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, sasPolicy, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Printf("Ctrl+Alt+Del policy: %v", err)
		return
	}
	defer k.Close()
	if _, _, err := k.GetIntegerValue("SoftwareSASGeneration"); err == nil {
		return // already set by someone else: leave it alone
	}
	if err := k.SetDWordValue("SoftwareSASGeneration", 1); err == nil {
		_ = os.WriteFile(filepath.Join(dataDir, "sas-policy-set"), []byte("1"), 0o600)
	}
}

func restoreSoftwareSAS(dataDir string) {
	if !fileExists(filepath.Join(dataDir, "sas-policy-set")) {
		return
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, sasPolicy, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue("SoftwareSASGeneration")
		k.Close()
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

var copyMu sync.Mutex

// copyFile copies through a temporary file, so a half-copied file is never left in place.
func copyFile(src, dst string) error {
	copyMu.Lock()
	defer copyMu.Unlock()
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
