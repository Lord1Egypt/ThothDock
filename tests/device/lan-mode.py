#!/usr/bin/env python3
"""ThothTerm LAN Mode against a real phone, from a desktop Chromium (Playwright).

    tests/device/lan-mode.py pair    http://PHONE:PORT/ PIN UPLOAD_DIR SHOTS
    tests/device/lan-mode.py lock    http://PHONE:PORT/ PIN
    tests/device/lan-mode.py expired http://PHONE:PORT/ PIN     (PIN older than its 2-minute life)

"pair" pairs through the real page, drives the terminal (typing, output,
resize), uploads the files of UPLOAD_DIR and the folder itself, reconnects
after the tab is closed, and signs out. It also checks the Host and Origin
rules. LAN Mode is plain HTTP on the local network: the PIN authenticates,
nothing is encrypted (the app says so); these tests do not pretend otherwise.
"""
import http.client
import json
import os
import random
import sys
import time
import urllib.parse

fails = 0


def check(ok, what):
    global fails
    print(("PASS: " if ok else "FAIL: ") + what)
    fails += 0 if ok else 1


def raw(url, method, path, body=None, headers=None, host=None):
    u = urllib.parse.urlsplit(url)
    c = http.client.HTTPConnection(u.hostname, u.port, timeout=15)
    c.putrequest(method, path, skip_host=True)
    c.putheader("Host", host or u.netloc)
    data = json.dumps(body).encode() if body is not None else None
    for k, v in (headers or {}).items():
        c.putheader(k, v)
    if data is not None:
        c.putheader("Content-Type", "application/json")
        c.putheader("Content-Length", str(len(data)))
    c.endheaders(data)
    r = c.getresponse()
    out = (r.status, r.read())
    c.close()
    return out


def screen(page):
    # xterm.js renders spaces as no-break spaces
    return page.locator(".xterm-rows").inner_text().replace("\u00a0", " ")


def run_cmd(page, cmd, marker, timeout=20):
    page.locator(".xterm-helper-textarea").focus()
    page.keyboard.type(cmd + "\n", delay=15)
    end = time.time() + timeout
    while time.time() < end:
        if marker in screen(page):
            return screen(page)
        time.sleep(0.3)
    return screen(page)


def pair_flow(url, pin, upload_dir, shots):
    from playwright.sync_api import sync_playwright
    origin = url.rstrip("/")
    st, _ = raw(url, "GET", "/", host="evil.example:7682")
    check(st >= 400, f"a foreign Host header is refused ({st})")
    st, _ = raw(url, "POST", "/api/pair", {"pin": "000000"}, {"Origin": "http://evil.example"})
    check(st == 403, f"pairing from a foreign Origin is refused ({st})")
    time.sleep(1.2)
    with sync_playwright() as p:
        b = p.chromium.launch()
        ctx = b.new_context(viewport={"width": 1100, "height": 700})
        page = ctx.new_page()
        errors = []
        page.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
        page.goto(url)
        page.wait_for_selector("#pin")
        page.screenshot(path=f"{shots}/lan-browser-pair.png")
        page.fill("#pin", pin)
        page.locator("#pair-form button[type=submit], #pair-form button").first.click()
        page.wait_for_selector(".xterm-rows", timeout=20000)
        token = page.evaluate("localStorage.getItem('thothterm.lan.token')")
        check(bool(token), "paired with the PIN from the phone; the browser holds a token")
        mark = "LANMARK%06d" % random.randint(0, 999999)
        out = run_cmd(page, f"echo {mark}$((1+1))", mark + "2")
        check(mark + "2" in out, "typing reaches the guest shell and its output comes back")
        out = run_cmd(page, "cat /etc/os-release | grep -m1 PRETTY", "PRETTY_NAME")
        check("Debian" in out, "the terminal is the phone's Debian guest")
        import re

        def size(tag):
            # wait for the output line, not the echoed command (which also contains the tag)
            run_cmd(page, f"clear; echo {tag} $(stty size)", f"{tag} $(")
            end = time.time() + 15
            while time.time() < end:
                found = re.findall(tag + r" (\d+ \d+)", screen(page))
                if found:
                    return found
                time.sleep(0.3)
            return []

        s1 = size("SIZE1")
        page.set_viewport_size({"width": 760, "height": 480})
        time.sleep(1.5)
        s2 = size("SIZE2")
        if not (s1 and s2):
            print("DEBUG screen:", [l for l in screen(page).splitlines() if "SIZE" in l or "stty" in l][:6])
        check(bool(s1) and bool(s2) and s1[-1] != s2[-1],
              f"resizing the browser resizes the guest terminal ({s1[:1]} -> {s2[:1]})")
        page.set_viewport_size({"width": 1100, "height": 700})
        page.screenshot(path=f"{shots}/lan-browser-terminal.png")
        # Upload: the files, then the folder.
        files = sorted(os.path.join(upload_dir, f) for f in os.listdir(upload_dir) if os.path.isfile(os.path.join(upload_dir, f)))
        page.set_input_files("#pick-files", files)
        page.wait_for_selector("#upload:not([hidden])", timeout=10000)
        target = (page.text_content("#upload-target") or "").strip()
        page.click("#upload-go")
        page.wait_for_function("() => true")
        end = time.time() + 60
        msg = ""
        while time.time() < end:
            msg = page.text_content("#upload-message") or ""
            if "upload" in msg.lower() and ("done" in msg.lower() or "uploaded" in msg.lower() or "saved" in msg.lower() or "fail" in msg.lower() or "error" in msg.lower()):
                break
            time.sleep(0.5)
        check("fail" not in msg.lower() and "error" not in msg.lower() and msg != "", f"file upload finished: {msg!r} into {target!r}")
        print("UPLOAD_TARGET=" + target)
        if page.locator("#upload-cancel").is_visible():
            page.click("#upload-cancel")
        try:
            page.set_input_files("#pick-folder", upload_dir)
            page.wait_for_selector("#upload:not([hidden])", timeout=10000)
            page.click("#upload-go")
            end = time.time() + 60
            while time.time() < end:
                msg = page.text_content("#upload-message") or ""
                if msg and ("done" in msg.lower() or "uploaded" in msg.lower() or "saved" in msg.lower() or "fail" in msg.lower() or "error" in msg.lower()):
                    break
                time.sleep(0.5)
            check("fail" not in msg.lower() and "error" not in msg.lower() and msg != "", f"folder upload finished: {msg!r}")
            if page.locator("#upload-cancel").is_visible():
                page.click("#upload-cancel")
        except Exception as e:  # Playwright needs a recent Chromium for directory inputs
            print(f"SKIP: folder upload through Playwright: {e}")
        # Interruption: close the tab, open a new one; the token reconnects without a PIN.
        page.close()
        page = ctx.new_page()
        page.goto(url)
        page.wait_for_selector(".xterm-rows", timeout=20000)
        check(page.locator("#pin").is_hidden(), "after the tab was closed, a new tab reconnects without the PIN")
        mark2 = "BACK%05d" % random.randint(0, 99999)
        check(mark2 in run_cmd(page, f"echo {mark2}", mark2), "the reconnected terminal works")
        st, _ = raw(url, "GET", "/api/session", headers={"Authorization": "Bearer " + token})
        check(st in (200, 204), f"the token is a live session ({st})")
        page.click("#signout")
        page.wait_for_selector("#pin", timeout=15000)
        st, _ = raw(url, "GET", "/api/session", headers={"Authorization": "Bearer " + token})
        check(st == 401, f"after sign-out the old token is refused ({st})")
        check(not [e for e in errors if "Content Security Policy" in e], "no CSP violation")
        b.close()


def lock_flow(url, pin):
    origin = url.rstrip("/")
    for _ in range(5):
        time.sleep(1.2)
        raw(url, "POST", "/api/pair", {"pin": "000000" if pin != "000000" else "111111"}, {"Origin": origin})
    time.sleep(1.2)
    st, body = raw(url, "POST", "/api/pair", {"pin": pin}, {"Origin": origin})
    check(st != 200, f"after five wrong PINs the right PIN no longer pairs ({st} {body[:60]!r})")


def coexist_flow(lan, pin, panel, code):
    """LAN Mode and the Web Panel running together: neither accepts the other's credential."""
    import ssl
    st, body = raw(lan, "POST", "/api/pair", {"pin": pin}, {"Origin": lan.rstrip("/")})
    check(st == 200, f"LAN pairing ({st})")
    lan_token = json.loads(body).get("token", "")
    u = urllib.parse.urlsplit(panel)
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    c = http.client.HTTPSConnection(u.hostname, u.port, context=ctx, timeout=15)
    c.request("POST", "/pair", json.dumps({"code": code}), {"Origin": panel.rstrip("/"), "Content-Type": "application/json"})
    r = c.getresponse()
    cookie = (r.getheader("Set-Cookie") or "").split(";")[0]
    r.read()
    check(r.status == 200 and cookie.startswith("td_session="), f"Web Panel pairing ({r.status})")
    c.request("GET", "/api/summary", headers={"Authorization": "Bearer " + lan_token, "Cookie": "td_session=" + lan_token})
    r = c.getresponse(); r.read()
    check(r.status == 401, f"the LAN token does not open the Web Panel ({r.status})")
    c.request("GET", "/api/summary", headers={"Cookie": cookie})
    r = c.getresponse(); r.read()
    check(r.status == 200, f"the Web Panel session works ({r.status})")
    c.close()
    panel_token = cookie.split("=", 1)[1]
    st, _ = raw(lan, "GET", "/api/session", headers={"Authorization": "Bearer " + panel_token})
    check(st == 401, f"the Web Panel session does not open LAN Mode ({st})")
    st, _ = raw(lan, "GET", "/api/session", headers={"Authorization": "Bearer " + lan_token})
    check(st in (200, 204), f"the LAN session works ({st})")
    raw(lan, "POST", "/api/logout", {}, {"Authorization": "Bearer " + lan_token, "Origin": lan.rstrip("/")})


def expired_flow(url, pin):
    st, body = raw(url, "POST", "/api/pair", {"pin": pin}, {"Origin": url.rstrip("/")})
    check(st != 200, f"a PIN past its two-minute life no longer pairs ({st} {body[:60]!r})")


if __name__ == "__main__":
    mode, url = sys.argv[1], sys.argv[2]
    if mode == "pair":
        pair_flow(url, sys.argv[3], sys.argv[4], sys.argv[5])
    elif mode == "coexist":
        coexist_flow(url, sys.argv[3], sys.argv[4], sys.argv[5])
    elif mode == "lock":
        lock_flow(url, sys.argv[3])
    else:
        expired_flow(url, sys.argv[3])
    print(f"lan-mode {mode}: {fails} failure(s)")
    sys.exit(1 if fails else 0)
