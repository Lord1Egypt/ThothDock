#!/usr/bin/env python3
"""Browser end-to-end check of the ThothDock Web Panel (tickets P6-02..P6-04).

Waits use locators, never string predicates: Playwright evaluates those with
eval() inside the page, which the panel's Content-Security-Policy refuses.

    tests/panel/e2e.py PANEL_URL PAIRING_JSON SCREENSHOT_DIR

The panel and an engine with the two-service Compose fixture running must be
up already (tests/panel/e2e.sh starts them). Needs Python Playwright with
Chromium. Prints PASS/FAIL lines; exits non-zero on any failure.
"""
import json
import sys

from playwright.sync_api import sync_playwright

url, pairing_path, shots = sys.argv[1], sys.argv[2], sys.argv[3]
fails = 0


def check(ok, what):
    global fails
    print(("PASS: " if ok else "FAIL: ") + what)
    if not ok:
        fails += 1


with sync_playwright() as p:
    browser = p.chromium.launch(args=["--host-resolver-rules=MAP phone.test 127.0.0.1"])
    page = browser.new_page(ignore_https_errors=True, viewport={"width": 1280, "height": 860})
    problems = []
    page.on("console", lambda m: problems.append(m.text) if m.type in ("error", "warning") else None)
    page.on("pageerror", lambda e: problems.append(str(e)))

    # The mistake users make: http:// on the HTTPS port.
    port = url.rsplit(":", 1)[1].rstrip("/")
    page.goto(f"http://127.0.0.1:{port}/")
    page.wait_for_selector("#pair:not([hidden])")
    check(page.url.startswith("https://127.0.0.1:"), f"http:// on the panel port lands on {page.url} (redirected, not an error)")
    explain = browser.new_page(ignore_https_errors=True, viewport={"width": 1000, "height": 700})
    explain.goto(f"http://phone.test:{port}/")
    check("uses HTTPS" in explain.text_content("body") and explain.url.startswith("http://"), "a hostname the panel cannot vouch for gets an explanation, not a redirect")
    explain.screenshot(path=f"{shots}/panel-http-misuse.png")
    explain.close()
    page.goto(url)
    page.wait_for_selector("#pair:not([hidden])")
    page.screenshot(path=f"{shots}/panel-pairing.png")
    check(page.is_visible("#pair") and not page.is_visible("#app"), "an unpaired browser sees only the pairing form")
    page.fill("#code", "0000 0000")
    page.click("#pair-form button")
    page.wait_for_selector("#pair-error:not(:empty)")
    check("wrong pairing code" in page.text_content("#pair-error"), "a wrong code is refused with a message")
    page.wait_for_timeout(1100)  # one attempt per second

    code = json.load(open(pairing_path))["code"]
    page.fill("#code", code[:4] + " " + code[4:])
    page.click("#pair-form button")
    page.wait_for_selector("#app:not([hidden])")
    page.wait_for_selector(".row")
    check(True, "pairing with the code from the phone opens the panel")
    page.screenshot(path=f"{shots}/panel-desktop-containers.png", full_page=True)

    names = page.locator(".row .name").all_text_contents()
    check("demo-api-1" in names and "demo-web-1" in names, f"containers listed ({', '.join(names)})")
    tiles = dict(zip(page.locator(".tile .l").all_text_contents(), page.locator(".tile .n").all_text_contents()))
    check(tiles.get("Running") == "2" and tiles.get("Stacks") == "1" and tiles.get("User networks") == "1", f"dashboard tiles {tiles}")
    check("127.0.0.1:" in page.text_content("#view"), "published ports are shown")
    check("Engine " in page.text_content("#engine-version") and "Paired" in page.text_content("#session-state"), "header shows the engine version and the session state")
    check(tiles.get("Volumes") is not None and tiles.get("Images") is not None, "summary cards include Images and Volumes")
    check(page.text_content("#fingerprint").count(":") == 31, "the certificate fingerprint is shown")

    # Stop the web service; the event stream refreshes the page by itself.
    web = page.locator(".row", has_text="demo-web-1")
    web.locator("button", has_text="Stop").click()
    page.locator(".row", has_text="demo-web-1").locator(".state", has_text="exited").wait_for(timeout=20000)
    check(True, "Stop updates the list through engine events")
    page.locator(".row", has_text="demo-web-1").locator("button", has_text="Start").click()
    page.locator(".row", has_text="demo-web-1").locator(".state", has_text="running").wait_for(timeout=20000)
    check(True, "Start brings it back")

    page.locator(".row", has_text="demo-api-1").locator("button", has_text="Shell").click()
    page.wait_for_selector("#shell[open]")
    check("docker exec -it demo-api-1 sh" in page.text_content("#shell-cmd"), "Shell shows the exact docker exec command")
    page.click("#shell-close")
    page.locator(".row", has_text="demo-api-1").locator("button", has_text="Logs").click()
    page.wait_for_selector("#logs[open]")
    page.locator("#logs-text", has_text="GET / HTTP").wait_for(timeout=10000)
    check("GET / HTTP" in page.text_content("#logs-text"), "the logs dialog shows the service's output")
    page.screenshot(path=f"{shots}/panel-desktop-logs.png")
    page.click("#logs-close")

    page.click("button[data-tab=stacks]")
    try:
        page.locator(".row", has_text="2 / 2 running").wait_for(timeout=10000)
        check(True, "the Stacks tab shows the Compose project")
    except Exception:
        check(False, "the Stacks tab shows the Compose project: " + page.text_content("#view"))
    page.click("button[data-tab=networks]")
    page.wait_for_selector(".row .name >> text=demo_default")
    view = page.text_content("#view")
    check("127.77.0.0/16" in view, "the Networks tab shows the user network and its subnet")
    check("User-defined" in view and "Built-in" in view, "the Networks tab separates user-defined from built-in networks")
    check("Unsupported" in view and "not implemented" in view.lower(), "none is shown as unsupported, not as a working network")
    check("not a linux docker bridge" in view.lower(), "bridge says it is not a Linux Docker bridge")
    check("not enforced isolation" in view, "the user network states that it is not enforced isolation")
    check(page.locator(".row.network").count() == 4, "the Networks tab lists bridge, host, none and the stack network")
    page.screenshot(path=f"{shots}/panel-desktop-networks.png", full_page=True)
    page.click("button[data-tab=events]")
    check("container" in page.text_content("#view") and "stop" in page.text_content("#view"), "the Events tab lists live events")

    phone = browser.new_page(ignore_https_errors=True, viewport={"width": 390, "height": 844}, device_scale_factor=2)
    phone.context.add_cookies(page.context.cookies())
    phone.goto(url)
    phone.wait_for_selector(".row")
    overflow = phone.evaluate("document.documentElement.scrollWidth > window.innerWidth")
    check(not overflow, "no horizontal scrolling at phone width")
    phone.screenshot(path=f"{shots}/panel-phone.png", full_page=True)
    phone.click("button[data-tab=networks]")
    phone.wait_for_selector(".row.network")
    check(not phone.evaluate("document.documentElement.scrollWidth > window.innerWidth"), "the Networks tab does not scroll sideways at phone width")
    phone.screenshot(path=f"{shots}/panel-phone-networks.png", full_page=True)

    page.click("#logout")
    page.wait_for_selector("#pair:not([hidden])")
    check(True, "Sign out returns to the pairing form")

    csp = [m for m in problems if "Content Security Policy" in m or "Refused" in m]
    other = [m for m in problems if m not in csp and "401" not in m and "Failed to load resource" not in m]
    check(not csp, "no Content-Security-Policy violation" + ("" if not csp else f": {csp[:3]}"))
    check(not other, "no script error in the console" + ("" if not other else f": {other[:3]}"))
    browser.close()

print(f"panel e2e: {fails} failure(s)")
sys.exit(1 if fails else 0)
