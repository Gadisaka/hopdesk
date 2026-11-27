"""End-to-end test: the real hopdesk server and viewer, driven in Chrome, with a
stand-in agent page that speaks the agent protocol.

    go build -o hopdesk-server ./cmd/server
    pip install playwright && playwright install chromium
    python3 test/e2e/run.py

Environment: HOPDESK_SERVER (server binary, default ./hopdesk-server),
CHROME (browser binary, default Playwright's Chromium),
SCREENSHOTS (folder to save screenshots in, default a temp folder).
"""
import os, subprocess, sys, time, json, tempfile
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
SERVER = os.environ.get("HOPDESK_SERVER", str(ROOT / "hopdesk-server"))
CHROME = os.environ.get("CHROME")
OUT = os.environ.get("SCREENSHOTS") or tempfile.mkdtemp()
os.makedirs(OUT, exist_ok=True)
AGENT_URL = (HERE / "stand-in-agent.html").as_uri()
results = []

def check(name, cond, detail=""):
    results.append((name, bool(cond), detail))
    print(("PASS " if cond else "FAIL ") + name + (f" ({detail})" if detail else ""), flush=True)

srv = subprocess.Popen([SERVER, "-addr", "127.0.0.1:8080", "-agent-key", "testkey123", "-stun", ""],
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
time.sleep(0.8)

try:
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=CHROME, headless=True, args=[
            "--disable-features=WebRtcHideLocalIpsWithMdns",
            "--autoplay-policy=no-user-gesture-required",
            "--force-device-scale-factor=1",
        ])
        actx = browser.new_context()
        agent = actx.new_page()
        agent.goto(AGENT_URL + "?key=testkey123&id=studio-mac")
        agent.wait_for_function("window.__registered === true", timeout=10000)
        check("agent registers with the right key", True)

        r = agent.evaluate("tryRegister('wrong-key', 'other')")
        check("server rejects a wrong agent key", r.get("message") == "bad agent key", r.get("message"))
        r = agent.evaluate("tryRegister('testkey123', 'studio-mac')")
        check("server refuses a duplicate device ID", r.get("message") == "id already online", r.get("message"))

        vctx = browser.new_context(viewport={"width": 1440, "height": 900}, accept_downloads=True)
        vctx.grant_permissions(["clipboard-read", "clipboard-write"], origin="http://127.0.0.1:8080")
        v = vctx.new_page()
        console = []
        v.on("console", lambda m: console.append(m.text))
        v.goto("http://127.0.0.1:8080/")
        check("viewer page is served", v.title() == "hopdesk")
        v.fill("#id", "studio-mac"); v.fill("#pw", "correct-horse-42")
        v.screenshot(path=f"{OUT}/login.png")

        # unknown device
        v.fill("#id", "nobody"); v.fill("#pw", "whatever1"); v.click("#connect")
        expect(v.locator("#status")).to_have_text("Error: that device is not online", timeout=10000)
        check("unknown device ID is reported", True)

        # wrong password
        v.fill("#id", "studio-mac"); v.fill("#pw", "not-the-password"); v.click("#connect")
        expect(v.locator("#status")).to_have_text("Wrong password", timeout=15000)
        check("wrong password is rejected", True)

        # connect
        v.fill("#id", "studio-mac"); v.fill("#pw", "correct-horse-42")
        v.click("#connect")
        expect(v.locator("#status")).to_have_text("Connected to studio-mac", timeout=15000)
        check("connects with the right password", True)
        v.wait_for_function("document.getElementById('video').videoWidth > 0", timeout=15000)
        vw = v.evaluate("[video.videoWidth, video.videoHeight]")
        check("video stream plays in the viewer", vw[0] > 0, f"{vw[0]}x{vw[1]}")
        check("display picker lists both displays", v.locator("#display option").count() == 2)

        # mouse
        # where the picture actually is inside the (letterboxed) video element
        box = v.evaluate("""() => { const r = video.getBoundingClientRect(), iw = video.videoWidth, ih = video.videoHeight;
            const s = Math.min(r.width / iw, r.height / ih), w = iw * s, h = ih * s;
            return { x: r.left + (r.width - w) / 2, y: r.top + (r.height - h) / 2, width: w, height: h }; }""")
        v.mouse.move(box["x"] + box["width"] * 0.30, box["y"] + box["height"] * 0.40)
        v.mouse.down(); v.mouse.up()
        time.sleep(0.3)
        log = agent.evaluate("window.__log")
        downs = [e for e in log if e["t"] == "down"]
        ok = downs and abs(downs[-1]["x"] - 0.30) < 0.02 and abs(downs[-1]["y"] - 0.40) < 0.02
        check("mouse click arrives at the right spot", ok, json.dumps(downs[-1]) if downs else "none")
        v.mouse.wheel(0, 240); time.sleep(0.3)
        check("scroll wheel arrives", any(e["t"] == "wheel" for e in agent.evaluate("window.__log")))

        # keyboard
        v.focus("#surface")
        v.keyboard.type("hello hopdesk")
        v.keyboard.press("Shift+KeyA")
        time.sleep(0.4)
        keys = [e for e in agent.evaluate("window.__log") if e["t"] == "key" and e["down"]]
        typed = "".join(e["key"] for e in keys if len(e["key"]) == 1)
        check("typing arrives as key events", typed.startswith("hello hopdesk"), typed)
        check("physical key codes are sent (for shortcuts)", any(e["code"] == "KeyH" for e in keys))
        check("modifier keys are sent", any(e["code"] == "ShiftLeft" for e in keys))

        # remote -> local clipboard (agent sends a clip 1.5s after auth)
        v.wait_for_function("navigator.clipboard.readText().then(t => window.__c = t), window.__c === 'copied on the remote computer'", timeout=8000)
        check("remote clipboard reaches this computer", True)

        # local -> remote clipboard via paste
        v.evaluate("navigator.clipboard.writeText('pasted from my laptop')")
        v.focus("#surface")
        v.keyboard.press("Control+KeyV")
        try:
            agent.wait_for_function("window.__clip === 'pasted from my laptop'", timeout=5000)
            pasted = True
        except Exception:
            pasted = False
        check("Ctrl+V sends the local clipboard first", pasted)
        log = agent.evaluate("window.__log")
        ci = next((i for i, e in enumerate(log) if e["t"] == "clip"), -1)
        vi = next((i for i, e in enumerate(log) if e["t"] == "key" and e["code"] == "KeyV" and e["down"]), -1)
        check("…and the V key arrives after the clipboard", ci >= 0 and vi > ci, f"clip#{ci} key#{vi}")

        # settings
        v.select_option("#quality", "high")
        v.select_option("#fps", "30")
        v.select_option("#display", "1")
        agent.wait_for_function("window.__settings && window.__settings.display === 1", timeout=5000)
        s = agent.evaluate("window.__settings")
        check("quality / fps / display changes reach the agent", s == {"display": 1, "quality": "high", "fps": 30}, json.dumps(s))
        time.sleep(0.6)
        v.evaluate("document.getElementById('toast').hidden = true")
        v.screenshot(path=f"{OUT}/display2.png")
        v.select_option("#display", "0"); v.select_option("#quality", "medium"); v.select_option("#fps", "15")
        time.sleep(0.6)

        # type something nice for the screenshot
        v.focus("#surface")
        for _ in range(25): v.keyboard.press("Backspace")
        v.keyboard.type("Hi from Addis!")
        v.mouse.move(box["x"] + box["width"] * 0.66, box["y"] + box["height"] * 0.47)
        v.wait_for_function("video.videoWidth >= 1200", timeout=20000)  # let the bitrate ramp up
        time.sleep(1.0)
        check("video reaches full resolution", True, v.evaluate("video.videoWidth + 'x' + video.videoHeight"))
        v.evaluate("document.getElementById('toast').hidden = true")
        v.screenshot(path=f"{OUT}/session.png")

        # files: browse
        v.click("#filesBtn")
        expect(v.locator("#list .row").first).to_be_visible(timeout=5000)
        check("remote file browser lists the home folder", v.locator("#path").inner_text().strip("\u200e") == "/Users/demo")
        v.locator("#list .row", has_text="Downloads").click()
        expect(v.locator("#path")).to_have_attribute("title", "/Users/demo/Downloads", timeout=5000)
        check("opening a folder works", True)

        # download
        with v.expect_download(timeout=15000) as dl:
            v.locator("#list .row", has_text="report.pdf").click()
        d = dl.value
        data = open(d.path(), "rb").read()
        expected = bytes(i % 251 for i in range(300000))
        check("download arrives complete and intact", data == expected and d.suggested_filename == "report.pdf",
              f"{len(data)} bytes, {d.suggested_filename}")

        # upload
        up = os.path.join(tempfile.mkdtemp(), "vacation-photo.jpg")
        payload = os.urandom(750_000)
        open(up, "wb").write(payload)
        v.set_input_files("#uploadInput", up)
        expect(v.locator(".xfer .msg", has_text="Saved to")).to_be_visible(timeout=15000)
        ups = agent.evaluate("window.__uploads")
        check("upload arrives complete", ups and ups[-1]["got"] == len(payload), json.dumps(ups[-1]) if ups else "none")
        time.sleep(0.6)
        v.evaluate("document.getElementById('toast').hidden = true")
        v.screenshot(path=f"{OUT}/files.png")

        # disconnect
        v.click("#disconnect")
        expect(v.locator("#status")).to_have_text("Not connected", timeout=5000)
        check("disconnect returns to the login screen", v.locator("#login").is_visible())

        errs = [c for c in console if "error" in c.lower()]
        check("no errors in the viewer console", not errs, "; ".join(errs[:3]))
        browser.close()
finally:
    srv.terminate()
    out = srv.communicate(timeout=5)[0]
    print("---- server log ----\n" + out)

failed = [r for r in results if not r[1]]
print(f"\n{len(results) - len(failed)}/{len(results)} checks passed")
sys.exit(1 if failed else 0)
