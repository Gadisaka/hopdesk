//go:build darwin

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const bundleID = "com.hopdesk.desktop"

// installUpdate replaces this HopDesk.app with the one in the downloaded .dmg
// and restarts. The swap is two renames in the same folder, so a failure
// leaves the old app in place.
func installUpdate(dmg string) error {
	bundle := appBundle()
	if bundle == "" {
		return errors.New("HopDesk isn't running from an app bundle")
	}
	parent := filepath.Dir(bundle)
	if !dirWritable(parent) {
		// e.g. a standard (non-admin) user with HopDesk in /Applications.
		_ = exec.Command("open", dmg).Start()
		return fmt.Errorf("HopDesk can't replace itself in %s, so the new version was opened for you: drag HopDesk to Applications and choose Replace", parent)
	}

	mnt, err := os.MkdirTemp("", "hopdesk-mount-")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	if out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg).CombinedOutput(); err != nil {
		return fmt.Errorf("couldn't open the update: %s", strings.TrimSpace(string(out)))
	}
	defer exec.Command("hdiutil", "detach", "-force", mnt).Run()

	src := filepath.Join(mnt, "HopDesk.app")
	id, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", filepath.Join(src, "Contents", "Info.plist")).Output()
	if err != nil || strings.TrimSpace(string(id)) != bundleID {
		return errors.New("the update doesn't contain HopDesk")
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", src).CombinedOutput(); err != nil {
		return fmt.Errorf("the new version's code signature is broken: %s", strings.TrimSpace(string(out)))
	}

	staged := filepath.Join(parent, ".HopDesk-update.app")
	_ = os.RemoveAll(staged)
	if out, err := exec.Command("ditto", src, staged).CombinedOutput(); err != nil {
		_ = os.RemoveAll(staged)
		return fmt.Errorf("couldn't copy the new version: %s", strings.TrimSpace(string(out)))
	}
	old := filepath.Join(parent, fmt.Sprintf(".HopDesk-old-%d.app", time.Now().Unix()))
	if err := os.Rename(bundle, old); err != nil {
		_ = os.RemoveAll(staged)
		return fmt.Errorf("couldn't move the old version aside: %w", err)
	}
	if err := os.Rename(staged, bundle); err != nil {
		_ = os.Rename(old, bundle)
		_ = os.RemoveAll(staged)
		return fmt.Errorf("couldn't put the new version in place: %w", err)
	}
	_ = os.RemoveAll(old) // this process keeps running from the open files
	_ = os.RemoveAll(filepath.Dir(dmg))

	log.Printf("update: installed; restarting")
	// Start the new version through Launch Services, then quit. "-restarted"
	// makes it wait for this copy to release the settings page's port.
	if err := exec.Command("open", "-n", "-a", bundle, "--args", "-app", "-restarted").Start(); err != nil {
		go restartSelf()
		return nil
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		quitApp()
	}()
	return nil
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".hopdesk-write-test-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
