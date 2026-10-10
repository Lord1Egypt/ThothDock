#!/usr/bin/env python3
"""Web Panel on a real phone from a desktop browser (P6-06).

    tests/panel/device-e2e.py https://PHONE:7690/ PAIRING_CODE SCREENSHOT_DIR CONTAINER

Pairs with the code shown on the phone, then stops and starts CONTAINER
through the panel and reads its logs. Needs Python Playwright + Chromium.
"""
import sys
from playwright.sync_api import sync_playwright

url, code, shots, name = sys.argv[1:5]
fails = 0


def check(ok, what):
    global fails
    print(("PASS: " if ok else "FAIL: ") + what)
    fails += 0 if ok else 1


with sync_playwright() as p:
    host = url.split("//")[1].split(":")[0]
    b = p.chromium.launch(args=[f"--host-resolver-rules=MAP phone.test {host}"])
    page = b.new_page(ignore_https_errors=True, viewport={"width": 1280, "height": 860})
    problems = []
    page.on("console", lambda m: problems.append(m.text) if m.type == "error" else None)
    port = url.rsplit(":", 1)[1].rstrip("/")
    page.goto(f"http://{host}:{port}/")
    page.wait_for_selector("#pair:not([hidden])")
    check(page.url.startswith("https://"), f"http:// typed by mistake lands on {page.url}")
    other = b.new_page(ignore_https_errors=True, viewport={"width": 1000, "height": 700})
    other.goto(f"http://phone.test:{port}/")
    check("uses HTTPS" in other.text_content("body"), "http:// with a name the panel cannot vouch for gets the explanation page")
    other.screenshot(path=f"{shots}/device-http-misuse.png")
    other.close()
    page.screenshot(path=f"{shots}/device-panel-pairing.png")
    page.fill("#code", code)
    page.click("#pair-form button")
    page.wait_for_selector("#app:not([hidden])", timeout=15000)
    page.locator(".row .name", has_text=name).first.wait_for(timeout=15000)
    check(True, "paired with the code from the phone; containers listed")
    tiles = dict(zip(page.locator(".tile .l").all_text_contents(), page.locator(".tile .n").all_text_contents()))
    check(int(tiles["Running"]) >= 1, f"dashboard {tiles}")
    check("Memory" in page.text_content("#host"), "device figures shown: " + page.text_content("#host")[:90])
    page.screenshot(path=f"{shots}/device-panel-containers.png", full_page=True)
    row = page.locator(".row", has_text=name)
    row.locator("button", has_text="Stop").click()
    page.locator(".row", has_text=name).locator(".state", has_text="exited").wait_for(timeout=30000)
    check(True, f"Stop {name} through the panel")
    page.locator(".row", has_text=name).locator("button", has_text="Start").click()
    page.locator(".row", has_text=name).locator(".state", has_text="running").wait_for(timeout=30000)
    check(True, f"Start {name} through the panel")
    page.click("button[data-tab=networks]")
    page.locator(".row .name", has_text="demo").wait_for(timeout=10000)
    check("127.77.0.0/16" in page.text_content("#view"), "Networks tab shows the user network")
    check("not enforced isolation" in page.text_content("#view") and "Unsupported" in page.text_content("#view"), "Networks tab states the limits")
    page.click("button[data-tab=events]")
    check("stop" in page.text_content("#view"), "live events received over the Wi-Fi link")
    check(not [m for m in problems if "Content Security" in m], "no CSP violation")
    page.click("#logout")
    page.wait_for_selector("#pair:not([hidden])")
    check(True, "sign out")
    b.close()
print(f"device panel e2e: {fails} failure(s)")
sys.exit(1 if fails else 0)
