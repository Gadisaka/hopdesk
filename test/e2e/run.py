"""End-to-end test: the real hopdesk server and viewer, driven in Chrome, with a
stand-in agent page that speaks the agent protocol (identity signature and
password proof included). Also attacks it: impostor agent, man in the middle,
cross-site WebSocket, connection flooding, wrong keys and passwords.

    go build -o hopdesk-server ./cmd/server
    pip install playwright && playwright install chromium
    python3 test/e2e/run.py

Environment: HOPDESK_SERVER (server binary, default ./hopdesk-server),
CHROME (browser binary, default Playwright's Chromium),
SCREENSHOTS (folder to save screenshots in, default a temp folder).
"""
import base64, json, os, socket, subprocess, sys, tempfile, time, urllib.request
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
SERVER = os.environ.get("HOPDESK_SERVER", str(ROOT / "hopdesk-server"))
CHROME = os.environ.get("CHROME")
OUT = os.environ.get("SCREENSHOTS") or tempfile.mkdtemp()
os.makedirs(OUT, exist_ok=True)
AGENT_URL = (HERE / "stand-in-agent.html").as_uri()
KEY = "testkey-0123456789abcdef"
PW = "correct-horse-42"
results = []


def check(name, cond, detail=""):
    results.append((name, bool(cond), detail))
    print(("PASS " if cond else "FAIL ") + name + (f" ({detail})" if detail else ""), flush=True)


# ---- a tiny raw WebSocket client, to attack the server directly ----
def ws_open(origin=None, host="127.0.0.1", port=8080):
    s = socket.create_connection((host, port), timeout=5)
    key = base64.b64encode(os.urandom(16)).decode()
    req = (f"GET /ws HTTP/1.1\r\nHost: {host}:{port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n")
    if origin:
        req += f"Origin: {origin}\r\n"
    s.sendall((req + "\r\n").encode())
    resp = b""
    while b"\r\n\r\n" not in resp:
        chunk = s.recv(4096)
        if not chunk:
            break
        resp += chunk
    return s, resp.split(b"\r\n", 1)[0].decode()


def ws_send(s, obj):
    data = json.dumps(obj).encode()
    mask = os.urandom(4)
    n = len(data)
    hdr = bytes([0x81]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + n.to_bytes(2, "big"))
    s.sendall(hdr + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))


def ws_recv(s):
    h = s.recv(2)
    n = h[1] & 0x7F
    if n == 126:
        n = int.from_bytes(s.recv(2), "big")
    data = b""
    while len(data) < n:
        data += s.recv(n - len(data))
    return json.loads(data)


def lan_ip():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("10.255.255.255", 1))
        return s.getsockname()[0]
    except OSError:
        return None
    finally:
        s.close()


# The stand-in agent is a file:// page, which browsers label Origin "null";
# the real Go agent sends no Origin at all. Allow "null" for this test only.
srv = subprocess.Popen([SERVER, "-addr", "0.0.0.0:8080", "-stun", "", "-allow-origin", "null"],
                       env={**os.environ, "HOPDESK_AGENT_KEY": KEY},
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
time.sleep(0.8)

try:
    # ---- server-level attacks (no browser needed) ----
    s, status = ws_open(origin="https://evil.example")
    s.close()
    check("server refuses WebSockets from other websites (CSWSH)", " 403 " in status, status)
    s, status = ws_open()
    s.close()
    check("server accepts the agent (no Origin)", " 101 " in status, status)
    with urllib.request.urlopen("http://127.0.0.1:8080/") as r:
        csp = r.headers.get("Content-Security-Policy", "")
        check("viewer page has a strict CSP", "script-src 'self'" in csp and "frame-ancestors 'none'" in csp)
        check("viewer page can't be framed (clickjacking)", r.headers.get("X-Frame-Options") == "DENY")

    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=CHROME, headless=True, args=[
            "--disable-features=WebRtcHideLocalIpsWithMdns",
            "--autoplay-policy=no-user-gesture-required",
            "--force-device-scale-factor=1",
        ])
        actx = browser.new_context()
        agent = actx.new_page()
        agent.goto(AGENT_URL + f"?key={KEY}&id=studio-mac")
        agent.wait_for_function("window.__registered === true && !!window.__fingerprint", timeout=10000)
        check("agent registers with the right key", True)
        r = agent.evaluate("tryRegister('wrong-key', 'other')")
        check("server rejects a wrong agent key", r.get("message") == "bad agent key", r.get("message"))
        r = agent.evaluate(f"tryRegister('{KEY}', 'studio-mac')")
        check("a second agent can't take an ID that's online", r.get("message") == "id already online", r.get("message"))

        vctx = browser.new_context(viewport={"width": 1440, "height": 900}, accept_downloads=True,
                                   color_scheme=os.environ.get("THEME", "dark"))
        vctx.grant_permissions(["clipboard-read", "clipboard-write"], origin="http://127.0.0.1:8080")
        v = vctx.new_page()
        console = []
        v.on("console", lambda m: console.append(m.text))
        v.on("pageerror", lambda e: console.append("pageerror: " + str(e)))
        v.goto("http://127.0.0.1:8080/")
        check("viewer page is served", v.title() == "hopdesk")
        expect(v.locator(".tour-pop")).to_be_visible(timeout=5000)
        check("first visit shows the quick tour", "Welcome" in v.locator("#tourTitle").inner_text())
        v.click(".tour-next"); time.sleep(0.3)
        check("…which points at the Device field", v.locator("#tourTitle").inner_text() == "Device")
        v.screenshot(path=f"{OUT}/tour-viewer.png")
        v.click(".tour-skip")
        check("…Skip closes it", v.locator(".tour-pop").count() == 0)
        v.reload(); time.sleep(0.9)
        check("…and it stays skipped after a reload", v.locator(".tour-pop").count() == 0)
        h = v.request.get("http://127.0.0.1:8080/help.html")
        check("the guide page is served", h.ok and "How to use HopDesk" in h.text() and "Every word explained" in h.text())
        v.fill("#id", "studio-mac"); v.fill("#pw", PW)
        v.screenshot(path=f"{OUT}/login.png")

        # unknown device
        v.fill("#id", "nobody"); v.click("#connect")
        expect(v.locator("#status")).to_have_text("Error: that device is not online", timeout=10000)
        check("unknown device ID is reported", True)

        # first connection: compare fingerprints; nothing password-derived is sent before that
        v.fill("#id", "studio-mac"); v.fill("#pw", "not-the-password"); v.click("#connect")
        expect(v.locator("#modal")).to_be_visible(timeout=15000)
        shown = v.locator("#mFp").inner_text()
        check("first connection shows the agent's fingerprint", shown == agent.evaluate("window.__fingerprint"), shown)
        check("no password proof is sent before you confirm", agent.evaluate("window.__auth2Count") == 0)
        v.screenshot(path=f"{OUT}/first-connect.png")
        v.click("#mOk")
        expect(v.locator("#status")).to_have_text("Wrong password", timeout=15000)
        check("wrong password is rejected", True)

        # right password: no prompt this time, the identity is remembered
        v.fill("#pw", PW); v.click("#connect")
        expect(v.locator("#status")).to_have_text("Connected to studio-mac", timeout=15000)
        check("connects with the right password, without asking again", v.locator("#modal").is_hidden())
        expect(v.locator(".tour-pop")).to_be_visible(timeout=6000)
        check("first session shows the controls tour", v.locator("#tourTitle").inner_text() == "You're connected")
        v.click(".tour-skip")
        check("password field is cleared after connecting", v.input_value("#pw") == "")
        v.wait_for_function("document.getElementById('video').videoWidth > 0", timeout=15000)
        check("video stream plays", True, v.evaluate("video.videoWidth + 'x' + video.videoHeight"))

        box = v.evaluate("""() => { const r = video.getBoundingClientRect(), iw = video.videoWidth, ih = video.videoHeight;
            const s = Math.min(r.width / iw, r.height / ih), w = iw * s, h = ih * s;
            return { x: r.left + (r.width - w) / 2, y: r.top + (r.height - h) / 2, width: w, height: h }; }""")
        v.mouse.move(box["x"] + box["width"] * 0.30, box["y"] + box["height"] * 0.40)
        v.mouse.down(); v.mouse.up(); time.sleep(0.3)
        downs = [e for e in agent.evaluate("window.__log") if e["t"] == "down"]
        check("mouse click arrives at the right spot", downs and abs(downs[-1]["x"] - 0.30) < 0.02 and abs(downs[-1]["y"] - 0.40) < 0.02,
              json.dumps(downs[-1]) if downs else "none")
        v.focus("#surface"); v.keyboard.type("hello"); v.keyboard.press("Shift+KeyA"); time.sleep(0.4)
        keys = [e for e in agent.evaluate("window.__log") if e["t"] == "key" and e["down"]]
        check("typing and modifier keys arrive", "".join(e["key"] for e in keys if len(e["key"]) == 1).startswith("hello")
              and any(e["code"] == "ShiftLeft" for e in keys))

        # remote -> local clipboard needs a click now
        expect(v.locator("#toastText")).to_contain_text("Remote copied", timeout=8000)
        before = v.evaluate("navigator.clipboard.readText()")
        check("remote clipboard is NOT written to yours automatically", before != "copied on the remote computer", repr(before))
        v.click("#toastBtn"); time.sleep(0.3)
        check("…one click copies it", v.evaluate("navigator.clipboard.readText()") == "copied on the remote computer")
        v.evaluate("navigator.clipboard.writeText('pasted from my laptop')")
        v.focus("#surface"); v.keyboard.press("Control+KeyV")
        try:
            agent.wait_for_function("window.__clip === 'pasted from my laptop'", timeout=5000); pasted = True
        except Exception:
            pasted = False
        check("Ctrl+V sends your clipboard to the remote", pasted)

        check("starts at high quality, 30 fps", v.input_value("#quality") == "high" and v.input_value("#fps") == "30")
        v.select_option("#quality", "ultra"); v.select_option("#fps", "60")
        agent.wait_for_function("window.__settings && window.__settings.fps === 60", timeout=5000)
        check("Ultra quality and 60 fps reach the agent", agent.evaluate("window.__settings")["quality"] == "ultra")
        v.select_option("#quality", "high"); v.select_option("#fps", "30")
        v.click("#filesBtn")
        v.locator("#list .row", has_text="Downloads").click()
        expect(v.locator("#path")).to_have_attribute("title", "/Users/demo/Downloads", timeout=5000)
        with v.expect_download(timeout=15000) as dl:
            v.locator("#list .row", has_text="report.pdf").click()
        data = open(dl.value.path(), "rb").read()
        check("download arrives intact", data == bytes(i % 251 for i in range(300000)), f"{len(data)} bytes")
        up = os.path.join(tempfile.mkdtemp(), "vacation-photo.jpg")
        payload = os.urandom(750_000)
        open(up, "wb").write(payload)
        v.set_input_files("#uploadInput", up)
        expect(v.locator(".xfer .msg", has_text="Saved to")).to_be_visible(timeout=15000)
        ups = agent.evaluate("window.__uploads")
        check("upload arrives intact", ups and ups[-1]["got"] == len(payload))
        v.click("#disconnect")
        expect(v.locator("#status")).to_have_text("Not connected", timeout=5000)

        # ---- a phone: touch gestures and the on-screen keyboard ----
        time.sleep(6.5)  # connection budget
        pctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=3, is_mobile=True,
                                   has_touch=True, bypass_csp=True, color_scheme=os.environ.get("THEME", "dark"))
        pctx.add_init_script("try { localStorage.setItem('hopdesk-tour-viewer-connect-v1', 'done') } catch (e) {}")
        ph = pctx.new_page()
        ph.goto("http://127.0.0.1:8080/")
        ph.fill("#id", "studio-mac"); ph.fill("#pw", PW); ph.tap("#connect")
        expect(ph.locator("#modal")).to_be_visible(timeout=15000)
        ph.tap("#mOk")
        expect(ph.locator("#touchbar")).to_be_visible(timeout=15000)
        check("phone: touch controls appear", ph.locator("#tcursor").is_visible())
        expect(ph.locator(".tour-pop")).to_be_visible(timeout=6000)
        check("phone: first session explains the gestures", ph.locator("#tourTitle").inner_text() == "Control it with your fingers")
        ph.screenshot(path=f"{OUT}/phone-tour.png")
        ph.tap(".tour-skip")
        ph.wait_for_function("video.videoWidth > 0", timeout=15000)
        ph.evaluate("""() => {
          window.__touch = async (steps) => {
            const s = document.getElementById('surface');
            const mk = (pts) => pts.map((p, i) => new Touch({ identifier: i, target: s, clientX: p[0], clientY: p[1] }));
            for (const st of steps) {
              const now = st.type === 'end' ? [] : mk(st.pts);
              s.dispatchEvent(new TouchEvent('touch' + st.type, { touches: now, targetTouches: now, changedTouches: mk(st.pts), bubbles: true, cancelable: true }));
              await new Promise((r) => setTimeout(r, st.wait || 20));
            }
          };
        }""")
        def gesture(steps):
            agent.evaluate("window.__log.length = 0")
            ph.evaluate("(s) => window.__touch(s)", steps)
            time.sleep(0.4)
            return agent.evaluate("window.__log")
        sr = ph.evaluate("(() => { const r = document.getElementById('surface').getBoundingClientRect(); return {x: r.x, y: r.y, w: r.width, h: r.height}; })()")
        cx, cy = sr["x"] + sr["w"] / 2, sr["y"] + sr["h"] / 2
        log = gesture([{"type": "start", "pts": [[cx, cy]]}] + [{"type": "move", "pts": [[cx + i * 10, cy]]} for i in range(1, 9)] + [{"type": "end", "pts": [[cx + 80, cy]]}])
        moves = [e for e in log if e["t"] == "move"]
        check("phone: dragging moves the pointer like a touchpad", moves and moves[-1]["x"] > 0.55 and not any(e["t"] == "down" for e in log),
              f"x={moves[-1]['x']:.2f}" if moves else "no moves")
        log = gesture([{"type": "start", "pts": [[cx, cy]], "wait": 40}, {"type": "end", "pts": [[cx, cy]]}])
        downs = [e for e in log if e["t"] == "down"]
        check("phone: a tap clicks where the pointer is", downs and downs[0]["b"] == 0 and downs[0]["x"] > 0.55)
        time.sleep(0.4)
        log = gesture([{"type": "start", "pts": [[cx - 30, cy], [cx + 30, cy]], "wait": 40}, {"type": "end", "pts": [[cx - 30, cy], [cx + 30, cy]]}])
        check("phone: a two-finger tap right-clicks", any(e["t"] == "down" and e["b"] == 2 for e in log))
        log = gesture([{"type": "start", "pts": [[cx - 30, cy], [cx + 30, cy]]}] + [{"type": "move", "pts": [[cx - 30, cy - i * 12], [cx + 30, cy - i * 12]]} for i in range(1, 8)] + [{"type": "end", "pts": [[cx - 30, cy - 84], [cx + 30, cy - 84]]}])
        wheels = [e for e in log if e["t"] == "wheel"]
        check("phone: a two-finger drag scrolls", wheels and sum(e["dy"] for e in wheels) > 0)
        gesture([{"type": "start", "pts": [[cx - 30, cy], [cx + 30, cy]]}] + [{"type": "move", "pts": [[cx - 30 - i * 15, cy], [cx + 30 + i * 15, cy]]} for i in range(1, 8)] + [{"type": "end", "pts": [[cx - 135, cy], [cx + 135, cy]]}])
        zoomed = ph.evaluate("video.style.transform")
        check("phone: pinching zooms in", "scale(" in zoomed and ph.locator("#tZoom").is_visible(), zoomed)
        ph.screenshot(path=f"{OUT}/phone-zoomed.png")
        ph.tap("#tZoom")
        check("phone: Fit zooms back out", ph.evaluate("video.style.transform") == "")

        ph.tap("#tKbd")
        expect(ph.locator("#kbar")).to_be_visible(timeout=3000)
        agent.evaluate("window.__log.length = 0")
        ph.keyboard.type("hi there"); time.sleep(0.4)
        typed = "".join(e["key"] for e in agent.evaluate("window.__log") if e["t"] == "key" and e["down"])
        check("phone: the keyboard types on the remote computer", typed == "hi there", typed)
        agent.evaluate("window.__log.length = 0")
        ph.tap("#kbar [data-mod=ctrl]"); ph.keyboard.type("c"); time.sleep(0.4)
        keys = [(e["code"], e["down"]) for e in agent.evaluate("window.__log") if e["t"] == "key"]
        check("phone: Ctrl then C sends Ctrl+C", keys == [("ControlLeft", True), ("KeyC", True), ("KeyC", False), ("ControlLeft", False)], keys)
        agent.evaluate("window.__log.length = 0")
        ph.tap("#kbar [data-code=Escape]"); time.sleep(0.3)
        check("phone: extra keys work (Esc)", any(e["t"] == "key" and e["code"] == "Escape" for e in agent.evaluate("window.__log")))
        ph.screenshot(path=f"{OUT}/phone-keyboard.png")
        ph.tap("#kDone")

        ph.tap("#tMode")
        check("phone: switches to direct mode", ph.locator("#tMode").get_attribute("data-mode") == "direct" and not ph.locator("#tcursor").is_visible())
        time.sleep(0.4)
        tx, ty = sr["x"] + sr["w"] * 0.25, cy
        log = gesture([{"type": "start", "pts": [[tx, ty]], "wait": 40}, {"type": "end", "pts": [[tx, ty]]}])
        downs = [e for e in log if e["t"] == "down"]
        expect_x = ph.evaluate(f"norm({{clientX: {tx}, clientY: {ty}}}).x")
        check("phone: direct mode clicks exactly where you tap", downs and abs(downs[0]["x"] - expect_x) < 0.01, f"{downs[0]['x']:.3f} vs {expect_x:.3f}" if downs else "")
        ph.tap("#tMode")  # back to touchpad for the next visit

        pic_w = "(() => { const r = pageRect(video); return Math.min(r.width / video.videoWidth, r.height / video.videoHeight) * video.videoWidth; })()"
        before_w = ph.evaluate(pic_w)
        ph.tap("#tRotate"); time.sleep(0.8)
        css_rot = ph.evaluate("document.body.classList.contains('rotated')")
        check("phone: Rotate turns the picture sideways", css_rot or ph.evaluate("!!document.fullscreenElement"))
        if css_rot:
            after_w = ph.evaluate(pic_w)
            check("phone: …and makes it bigger", after_w > before_w * 1.5, f"{before_w:.0f}px → {after_w:.0f}px wide")
            # Tap a known spot of the turned picture, worked out from the screen geometry alone.
            g = ph.evaluate("(() => { const r = document.getElementById('surface').getBoundingClientRect(); return {right: r.right, top: r.top, w: r.width, h: r.height, vw: video.videoWidth, vh: video.videoHeight}; })()")
            W, H = g["h"], g["w"]  # sideways: the surface's long side runs down the phone
            k = min(W / g["vw"], H / g["vh"]); pw, phh = g["vw"] * k, g["vh"] * k
            a, b = (W - pw) / 2 + 0.1 * pw, (H - phh) / 2 + 0.2 * phh
            sx, sy = g["right"] - b, g["top"] + a
            ph.tap("#tMode"); time.sleep(0.3)
            log = gesture([{"type": "start", "pts": [[sx, sy]], "wait": 40}, {"type": "end", "pts": [[sx, sy]]}])
            downs = [e for e in log if e["t"] == "down"]
            check("phone: sideways, a tap lands exactly where you tap", downs and abs(downs[0]["x"] - 0.1) < 0.02 and abs(downs[0]["y"] - 0.2) < 0.02,
                  f"x={downs[0]['x']:.3f} y={downs[0]['y']:.3f}" if downs else "no click")
            ph.tap("#tMode"); time.sleep(0.4)
            mx, my = g["right"] - g["w"] / 2, g["top"] + g["h"] / 2
            log = gesture([{"type": "start", "pts": [[mx, my]]}] + [{"type": "move", "pts": [[mx, my + i * 10]]} for i in range(1, 9)] + [{"type": "end", "pts": [[mx, my + 80]]}])
            mv = [e for e in log if e["t"] == "move"]
            check("phone: sideways, dragging moves the pointer the right way", len(mv) >= 2 and mv[-1]["x"] - mv[0]["x"] > 0.02 and abs(mv[-1]["y"] - mv[0]["y"]) < 0.01,
                  f"dx={mv[-1]['x'] - mv[0]['x']:.3f} dy={mv[-1]['y'] - mv[0]['y']:.3f}" if len(mv) >= 2 else "no moves")
            ph.screenshot(path=f"{OUT}/phone-rotated.png")
        ph.tap("#tRotate"); time.sleep(0.5)
        check("phone: Upright turns it back", not ph.evaluate("document.body.classList.contains('rotated')") and not ph.evaluate("!!document.fullscreenElement"))

        ph.tap("#moreBtn")
        check("phone: picture settings open from one button", ph.locator("#quality").is_visible())
        ph.screenshot(path=f"{OUT}/phone-settings.png")
        ph.tap("#crumbName")
        ph.screenshot(path=f"{OUT}/phone-session.png")
        ph.tap("#disconnect")
        expect(ph.locator("#status")).to_have_text("Not connected", timeout=5000)
        check("phone: touch controls hide after disconnecting", not ph.locator("#touchbar").is_visible())
        pctx.close()

        # ---- attack: man in the middle ----
        # Seen through an attacker, the real agent signs a different connection binding.
        time.sleep(6.5)  # the per-address connection budget refills one attempt every 6 s
        agent.evaluate("window.__simulateMitm = true")
        n_before = agent.evaluate("window.__auth2Count")
        v.fill("#pw", PW); v.click("#connect")
        expect(v.locator("#status")).to_contain_text("could not prove its identity", timeout=15000)
        check("man in the middle is detected; no password proof is sent", agent.evaluate("window.__auth2Count") == n_before)
        agent.evaluate("window.__simulateMitm = false")

        # ---- attack: an impostor takes the ID while the real agent is offline ----
        agent.close()
        time.sleep(0.5)
        imp = actx.new_page()
        imp.goto(AGENT_URL + f"?key={KEY}&id=studio-mac")
        imp.wait_for_function("window.__registered === true", timeout=10000)
        time.sleep(6.5)
        v.fill("#pw", PW); v.click("#connect")
        expect(v.locator("#modal.danger")).to_be_visible(timeout=15000)
        check("impostor agent is detected (identity changed warning)", "identity changed" in v.locator("#mTitle").inner_text().lower())
        v.screenshot(path=f"{OUT}/impostor-warning.png")
        check("impostor never receives a password proof", imp.evaluate("window.__auth2Count") == 0)
        v.click("#mCancel")
        expect(v.locator("#status")).to_contain_text("identity changed", timeout=5000)

        # ---- the viewer refuses to run on an insecure (plain HTTP, non-local) page ----
        ip = lan_ip()
        if ip:
            v2 = vctx.new_page()
            v2.goto(f"http://{ip}:8080/")
            check("viewer refuses to run over plain HTTP", v2.locator("#connect").is_disabled()
                  and "HTTPS" in v2.locator("#status").inner_text(), ip)
            v2.close()

        errs = [c for c in console if "error" in c.lower() and "favicon" not in c.lower()]
        check("no errors in the viewer console", not errs, "; ".join(errs[:3]))
        browser.close()

    # ---- attack: flooding connection attempts ----
    replies = []
    for _ in range(8):
        s, _ = ws_open()
        ws_send(s, {"type": "offer", "id": "nobody", "sdp": "x"})
        replies.append(ws_recv(s).get("message", ""))
        s.close()
    limited = sum("too many" in r for r in replies)
    check("a flood of connection attempts is rate-limited", limited >= 3, f"{limited}/8 refused")

    # ---- attack: guessing the agent key ----
    msgs = []
    for _ in range(6):
        s, _ = ws_open()
        ws_send(s, {"type": "register", "id": "x", "key": "guess"})
        msgs.append(ws_recv(s).get("message", ""))
        s.close()
    check("agent-key guessing gets locked out", msgs[-1].startswith("too many wrong agent keys"), msgs[-1])
finally:
    srv.terminate()
    out = srv.communicate(timeout=5)[0]
    print("---- server log ----\n" + out)

failed = [r for r in results if not r[1]]
print(f"\n{len(results) - len(failed)}/{len(results)} checks passed")
sys.exit(1 if failed else 0)
