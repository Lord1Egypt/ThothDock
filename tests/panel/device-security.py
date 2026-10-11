#!/usr/bin/env python3
"""Web Panel security checks against a real phone (stdlib only).

    tests/panel/device-security.py session URL CODE FINGERPRINT CONTAINER
    tests/panel/device-security.py exhaust URL CODE

"session" pairs with CODE and checks the certificate, unauthenticated access,
single-use codes, cookie flags, Origin/CSRF, Host validation, an allowed action
on CONTAINER (stop then start) and sign-out. "exhaust" spends five wrong codes
and shows that CODE is then dead, and that tries faster than one a second are
refused. URL is the https:// address (an adb forward of the phone's panel).
"""
import hashlib
import http.client
import json
import ssl
import sys
import time
import urllib.parse

fails = 0


def check(ok, what):
    global fails
    print(("PASS: " if ok else "FAIL: ") + what)
    fails += 0 if ok else 1


def conn(url):
    u = urllib.parse.urlsplit(url)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE  # self-signed: identity is the fingerprint, checked below
    return http.client.HTTPSConnection(u.hostname, u.port, context=ctx, timeout=20), u


def req(url, method, path, body=None, headers=None, host=None):
    c, u = conn(url)
    h = dict(headers or {})
    if body is not None:
        h["Content-Type"] = "application/json"
    c.putrequest(method, path, skip_host=True)
    c.putheader("Host", host or u.netloc)
    for k, v in h.items():
        c.putheader(k, v)
    data = json.dumps(body).encode() if body is not None else None
    if data:
        c.putheader("Content-Length", str(len(data)))
    c.endheaders(data)
    r = c.getresponse()
    out = (r.status, r.getheaders(), r.read())
    der = c.sock.getpeercert(binary_form=True) if c.sock else None
    c.close()
    return out + (der,)


def fp(der):
    return ":".join("%02X" % b for b in hashlib.sha256(der).digest())


def session(url, code, fingerprint, container):
    origin = url.rstrip("/")
    st, _, _, der = req(url, "GET", "/")
    check(st == 200 and fp(der) == fingerprint.replace(" ", "").upper(),
          "the served certificate matches the fingerprint the phone shows")
    for path in ("/api/summary", "/api/containers", "/api/networks", "/api/events"):
        st, _, _, _ = req(url, "GET", path)
        check(st == 401, f"unauthenticated GET {path} is refused ({st})")
    st, _, _, _ = req(url, "POST", f"/api/containers/{container}/stop", {}, {"Origin": origin})
    check(st == 401, f"unauthenticated stop is refused ({st})")
    st, _, _, _ = req(url, "GET", "/", host="evil.example")
    check(st == 421, f"a foreign Host header (DNS rebinding) is refused ({st})")
    st, _, _, _ = req(url, "POST", "/pair", {"code": code}, {"Origin": "https://evil.example"})
    check(st == 403, f"pairing from a foreign Origin is refused ({st})")
    time.sleep(1.1)
    st, hdrs, body, _ = req(url, "POST", "/pair", {"code": code}, {"Origin": origin})
    check(st == 200, f"pairing with the code from the phone ({st})")
    cookie = [v for k, v in hdrs if k.lower() == "set-cookie"]
    raw = cookie[0] if cookie else ""
    check(all(f in raw for f in ("HttpOnly", "Secure", "SameSite=Strict")), "session cookie is HttpOnly, Secure, SameSite=Strict")
    token = raw.split(";")[0]
    csrf = json.loads(body).get("csrf", "")
    time.sleep(1.1)
    st, _, _, _ = req(url, "POST", "/pair", {"code": code}, {"Origin": origin})
    check(st in (401, 403), f"the same code cannot pair a second browser ({st})")
    auth = {"Cookie": token}
    st, _, body, _ = req(url, "GET", "/api/summary", headers=auth)
    check(st == 200 and b"userNetworks" in body, "the session reads the summary")
    path = f"/api/containers/{container}/stop"
    st, _, _, _ = req(url, "POST", path, {}, dict(auth, Origin=origin))
    check(st == 403, f"a state change without the CSRF token is refused ({st})")
    st, _, _, _ = req(url, "POST", path, {}, dict(auth, Origin="https://evil.example", **{"X-ThothDock-CSRF": csrf}))
    check(st == 403, f"a state change from a foreign Origin is refused ({st})")
    st, _, _, _ = req(url, "POST", path, {}, dict(auth, Origin=origin, **{"X-ThothDock-CSRF": "x" * 43}))
    check(st == 403, f"a wrong CSRF token is refused ({st})")
    st, _, _, _ = req(url, "POST", path, {}, dict(auth, Origin=origin, **{"X-ThothDock-CSRF": csrf}))
    check(st in (200, 204), f"stop {container} with Origin and CSRF ({st})")
    st, _, _, _ = req(url, "POST", f"/api/containers/{container}/start", {}, dict(auth, Origin=origin, **{"X-ThothDock-CSRF": csrf}))
    check(st in (200, 204), f"start {container} again ({st})")
    st, _, _, _ = req(url, "POST", "/api/containers/x/exec", {}, dict(auth, Origin=origin, **{"X-ThothDock-CSRF": csrf}))
    check(st in (400, 403, 404), f"an action outside the allow list is refused ({st})")
    st, _, _, _ = req(url, "POST", "/api/logout", {}, dict(auth, Origin=origin, **{"X-ThothDock-CSRF": csrf}))
    check(st in (200, 204), f"sign out ({st})")
    st, _, _, _ = req(url, "GET", "/api/summary", headers=auth)
    check(st == 401, f"the signed-out session is dead ({st})")


def exhaust(url, code):
    origin = url.rstrip("/")
    wrong = "00000000" if code != "00000000" else "11111111"
    st1, _, _, _ = req(url, "POST", "/pair", {"code": wrong}, {"Origin": origin})
    st2, _, b2, _ = req(url, "POST", "/pair", {"code": wrong}, {"Origin": origin})
    check(st2 == 429 or b"too many" in b2.lower(), f"a second try within a second is refused ({st2})")
    for _ in range(4):
        time.sleep(1.1)
        req(url, "POST", "/pair", {"code": wrong}, {"Origin": origin})
    time.sleep(1.1)
    st, _, body, _ = req(url, "POST", "/pair", {"code": code}, {"Origin": origin})
    check(st != 200, f"after five wrong codes the right code no longer pairs ({st} {body[:60]!r})")


if __name__ == "__main__":
    mode = sys.argv[1]
    if mode == "session":
        session(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5])
    else:
        exhaust(sys.argv[2], sys.argv[3])
    print(f"device panel security ({mode}): {fails} failure(s)")
    sys.exit(1 if fails else 0)
