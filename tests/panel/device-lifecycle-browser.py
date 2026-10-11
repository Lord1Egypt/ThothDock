#!/usr/bin/env python3
"""The owner's reproduction in a real desktop browser, with screenshots.

    ADB="adb -s SERIAL" PKG=PACKAGE tests/panel/device-lifecycle-browser.py https://PHONE:7690/ SHOT_DIR

Pairs a browser, shows the dashboard, then stops and starts the Web Panel from
the Android UI, reloads the same tab and requires the pairing screen. The
pairing code is read from the app's private pairing file over adb and never
printed. Needs Python Playwright + Chromium.
"""
import os
import re
import subprocess
import sys

from playwright.sync_api import sync_playwright

url, shots = sys.argv[1:3]
adb = os.environ.get("ADB", "adb").split()
pkg = os.environ.get("PKG", "com.thothterm.debian.qa.nextgen")
uia = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "device", "uia.sh")
fails = 0


def check(ok, what):
    global fails
    print(("PASS: " if ok else "FAIL: ") + what)
    fails += 0 if ok else 1


def code():
    out = subprocess.run(adb + ["shell", f"run-as {pkg} cat files/thothdock/panel/pairing.json"], capture_output=True, text=True).stdout
    m = re.search(r'"code":\s*"(\d+)"', out)
    return m.group(1) if m else ""


def tap(label):
    subprocess.run(["bash", uia, "top"], capture_output=True)
    for _ in range(6):
        if subprocess.run(["bash", uia, "tap", label], capture_output=True).returncode == 0:
            import time
            time.sleep(5)
            return True
        subprocess.run(["bash", uia, "scroll"], capture_output=True)
    return False


with sync_playwright() as p:
    b = p.chromium.launch()
    page = b.new_page(ignore_https_errors=True, viewport={"width": 1280, "height": 860})
    page.goto(url)
    page.wait_for_selector("#pair:not([hidden])")
    check(tap("New code"), "new pairing code on the phone")
    page.fill("#code", code())
    page.click("#pair-form button")
    page.wait_for_selector("#app:not([hidden])", timeout=15000)
    page.screenshot(path=f"{shots}/1-paired-dashboard.png")
    check(True, "browser paired; dashboard shown")
    check(tap("Stop Web Panel"), "Stop Web Panel on the phone")
    check(tap("Start Web Panel"), "Start Web Panel on the phone")
    resp = page.reload(wait_until="domcontentloaded")
    page.wait_for_selector("#pair:not([hidden])", timeout=15000)
    check(page.is_hidden("#app"), "the old tab shows no dashboard after the restart")
    page.screenshot(path=f"{shots}/2-same-tab-after-restart-pairing-required.png")
    check(page.is_visible("#pair"), "the pairing screen is shown to the previously paired browser")
    page.fill("#code", code())
    page.click("#pair-form button")
    page.wait_for_selector("#app:not([hidden])", timeout=15000)
    page.screenshot(path=f"{shots}/3-paired-again.png")
    check(True, "pairing with the new code gives a working dashboard")
    b.close()
sys.exit(1 if fails else 0)
