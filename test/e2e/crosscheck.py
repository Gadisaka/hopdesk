"""Checks that the viewer's WebCrypto code (viewer.js) and the Go agent's code
(internal/auth) agree exactly: Go writes a test vector, the browser checks it.

    go build -o hopdesk-server ./cmd/server
    python3 test/e2e/crosscheck.py
"""
import json, os, subprocess, sys, tempfile, time
from pathlib import Path
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parent.parent.parent
SERVER = os.environ.get("HOPDESK_SERVER", str(ROOT / "hopdesk-server"))
vec_path = os.path.join(tempfile.mkdtemp(), "vector.json")
subprocess.run(["go", "test", "./internal/auth", "-run", "TestWriteVector", "-count=1"],
               cwd=ROOT, check=True, env={**os.environ, "HOPDESK_VECTOR": vec_path})
vec = json.load(open(vec_path))

srv = subprocess.Popen([SERVER, "-addr", "127.0.0.1:8081", "-stun", ""],
                       env={**os.environ, "HOPDESK_AGENT_KEY": "crosscheck-0123456789"},
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(0.6)
ok = True
try:
    with sync_playwright() as p:
        b = p.chromium.launch(executable_path=os.environ.get("CHROME"))
        pg = b.new_page()
        pg.goto("http://127.0.0.1:8081/")
        r = pg.evaluate("""async (v) => {
            const A = window.hopAuth;
            const bind = A.binding(v.viewerSDP, v.agentSDP);
            const tampered = v.sig.slice(0, 10) + (v.sig[10] === 'A' ? 'B' : 'A') + v.sig.slice(11);
            return {
                binding: bind === v.binding,
                signature: await A.verifyAgent(v.pub, bind, v.viewerNonce, v.sig),
                tamperedRejected: !(await A.verifyAgent(v.pub, bind, v.viewerNonce, tampered)),
                otherConnRejected: !(await A.verifyAgent(v.pub, bind.replace('DD', 'EE'), v.viewerNonce, v.sig)),
                proof: (await A.makeProof(v.password, v.salt, v.iter, bind, v.viewerNonce, v.agentNonce)) === v.proof,
                wrongPwDiffers: (await A.makeProof('wrong', v.salt, v.iter, bind, v.viewerNonce, v.agentNonce)) !== v.proof,
                fingerprint: (await A.fingerprint(v.pub)) === v.fingerprint,
            };
        }""", vec)
        b.close()
    for k, v in r.items():
        print(("PASS " if v else "FAIL ") + "Go and browser agree: " + k)
        ok &= v
finally:
    srv.terminate()
sys.exit(0 if ok else 1)
