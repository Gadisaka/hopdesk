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
