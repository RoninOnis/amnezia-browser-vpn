#!/usr/bin/env python3
"""Real-browser end-to-end test (optional, needs a Firefox that can load an
unsigned extension + geckodriver).

What it proves: inside an actual Firefox, `proxy.onRequest` sends the hosts that
match the active list through the helper's SOCKS5 tunnel and everything else
direct — i.e. browser-scoped split tunnelling works, not just the Go helper.

Prerequisites
-------------
* Firefox Developer Edition / Nightly (release builds ignore
  `xpinstall.signatures.required=false`), path in $FF_BIN
* geckodriver, path in $GECKODRIVER
* a built helper: dist/awgbv-linux-amd64 (or $AWGBV_BIN)
* a real AmneziaWG profile: vpn:// link in a file, or a .conf path

Usage
-----
    python3 tests/browser_e2e.py profile.link \\
        --mode whitelist --whitelist ipinfo.io \\
        --expect-tunnel-url https://ipinfo.io/ip \\
        --expect-direct-url https://api.ipify.org

Note: the test seeds the extension's settings (enabled + profile + lists) into a
throwaway copy of the extension, so it exercises the proxy path without needing
to click through the popup.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request
import zipfile

FF = os.environ.get("FF_BIN", "/opt/fftest/firefox/firefox")
GD = os.environ.get("GECKODRIVER", "/opt/fftest/geckodriver")
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.environ.get("AWGBV_BIN", os.path.join(ROOT, "dist", "awgbv-linux-amd64"))
STATE = "/tmp/awgbv-browser-test"
PROFILE = "/tmp/awgbv-ffprofile"
TESTEXT = "/tmp/awgbv-test-ext"
NM_DIR = os.path.expanduser("~/.mozilla/native-messaging-hosts")
ADDON_ID = "awgbv@0xoni.ru"


class WebDriver:
    def __init__(self, port=4444):
        self.base = f"http://127.0.0.1:{port}"

    def _open(self, req):
        with urllib.request.urlopen(req, timeout=180) as r:
            return json.load(r)

    def post(self, path, payload):
        return self._open(urllib.request.Request(
            self.base + path, data=json.dumps(payload).encode(), method="POST",
            headers={"Content-Type": "application/json"}))

    def put(self, path, payload):
        return self._open(urllib.request.Request(
            self.base + path, data=json.dumps(payload).encode(), method="PUT",
            headers={"Content-Type": "application/json"}))

    def get(self, path):
        return self._open(urllib.request.Request(self.base + path))


def build_test_extension(link, profile, whitelist, blacklist):
    shutil.rmtree(TESTEXT, ignore_errors=True)
    shutil.copytree(os.path.join(ROOT, "extension"), TESTEXT,
                    ignore=shutil.ignore_patterns("tests"))
    bg_path = os.path.join(TESTEXT, "background.js")
    src = open(bg_path, encoding="utf-8").read()
    defaults = {
        "enabled": True,
        "autoConnect": True,
        "whitelist": whitelist,
        "blacklist": blacklist,
        "profiles": [{
            "id": "e2e",
            "name": profile["name"],
            "endpoint": f"{profile['host']}:{profile['port']}",
            "container": profile.get("container", ""),
            "clientIp": profile["clientIp"],
            "addedAt": 0,
            "link": link,
            "profile": profile,
        }],
        "activeProfileId": "e2e",
        "listenPort": 0,
    }
    src, n = re.subn(r"const DEFAULTS = \{.*?\n\};",
                     "const DEFAULTS = " + json.dumps(defaults, ensure_ascii=False) + ";",
                     src, count=1, flags=re.S)
    if n != 1:
        raise RuntimeError("DEFAULTS block not found in background.js")
    open(bg_path, "w", encoding="utf-8").write(src)

    xpi = "/tmp/awgbv-e2e.xpi"
    with zipfile.ZipFile(xpi, "w", zipfile.ZIP_DEFLATED) as z:
        for root, _dirs, files in os.walk(TESTEXT):
            for f in files:
                p = os.path.join(root, f)
                z.write(p, os.path.relpath(p, TESTEXT))
    return xpi


def prepare_profile(xpi):
    shutil.rmtree(PROFILE, ignore_errors=True)
    os.makedirs(os.path.join(PROFILE, "extensions"))
    shutil.copy(xpi, os.path.join(PROFILE, "extensions", f"{ADDON_ID}.xpi"))
    prefs = {
        "xpinstall.signatures.required": False,
        "extensions.autoDisableScopes": 0,
        "extensions.enabledScopes": 15,
        "browser.shell.checkDefaultBrowser": False,
        "browser.aboutwelcome.enabled": False,
        "datareporting.policy.dataSubmissionEnabled": False,
        "toolkit.telemetry.enabled": False,
        "network.trr.mode": 0,
        "app.update.auto": False,
        "browser.startup.page": 0,
    }
    with open(os.path.join(PROFILE, "user.js"), "w") as f:
        for k, v in prefs.items():
            f.write(f"user_pref({json.dumps(k)}, {json.dumps(v)});\n")


def install_native_manifest():
    os.makedirs(NM_DIR, exist_ok=True)
    with open(os.path.join(NM_DIR, "awgbv.json"), "w") as f:
        json.dump({
            "name": "awgbv",
            "description": "Amnezia Browser VPN helper",
            "path": BIN,
            "type": "stdio",
            "allowed_extensions": [ADDON_ID],
        }, f, indent=2)


def start_geckodriver():
    proc = subprocess.Popen([GD, "--port", "4444", "--log", "error"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            env={**os.environ, "AWGBV_STATE_DIR": STATE})
    for _ in range(60):
        try:
            urllib.request.urlopen("http://127.0.0.1:4444/status", timeout=1).read()
            return proc
        except Exception:
            time.sleep(0.5)
    raise RuntimeError("geckodriver did not start")


def egress_ip(wd, session, url):
    """Fetch an "what is my IP" style URL inside the browser and return its body.

    The body must be the client IP as seen by that host, so the URL has to point
    at an echo service (ipify, ipinfo, ifconfig.me/ip, ...).
    """
    wd.post(f"/session/{session}/url", {"url": url})
    time.sleep(1)
    src = wd.get(f"/session/{session}/source")["value"]
    return re.sub(r"<[^>]+>", "", src).strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("profile", help="vpn:// link file, .conf or JSON profile")
    # режим больше не задаётся: он выводится из списков (непустой whitelist → «только список»)
    ap.add_argument("--whitelist", default="")
    ap.add_argument("--blacklist", default="")
    ap.add_argument("--expect-tunnel-url", action="append", default=[], metavar="URL",
                    help="echo-IP URL whose answer must be the VPN server IP (repeatable)")
    ap.add_argument("--expect-direct-url", action="append", default=[], metavar="URL",
                    help="echo-IP URL whose answer must NOT be the VPN server IP (repeatable)")
    ap.add_argument("--wait", type=int, default=8, help="seconds for the tunnel to come up")
    args = ap.parse_args()

    whitelist = [x for x in args.whitelist.split(",") if x]
    blacklist = [x for x in args.blacklist.split(",") if x]
    link = open(args.profile, encoding="utf-8").read().strip()
    prof = json.loads(subprocess.run([BIN, "--dump-profile", args.profile],
                                     capture_output=True, text=True, check=True).stdout)
    server = prof["host"]
    print(f"profile {prof['name']} → {server}:{prof['port']} | "
          f"whitelist={whitelist} blacklist={blacklist}")

    xpi = build_test_extension(link, prof, whitelist, blacklist)
    prepare_profile(xpi)
    install_native_manifest()
    shutil.rmtree(STATE, ignore_errors=True)
    env = {**os.environ, "AWGBV_STATE_DIR": STATE}
    subprocess.run([BIN, "--stop"], env=env, capture_output=True)

    gd = start_geckodriver()
    wd = WebDriver()
    ok = True
    try:
        sess = wd.post("/session", {"capabilities": {"alwaysMatch": {
            "browserName": "firefox",
            "moz:firefoxOptions": {
                "binary": FF,
                "args": ["-headless", "-no-remote", "-profile", PROFILE],
                "log": {"level": "error"},
            },
        }}})["value"]["sessionId"]
        time.sleep(args.wait)

        st = json.loads(subprocess.run([BIN, "--status"], capture_output=True,
                                       text=True, env=env).stdout or "{}")
        print(f"helper: state={st.get('state')} socks=127.0.0.1:{st.get('socksPort')} "
              f"handshake={st.get('lastHandshakeUnix')}")
        if not st.get("running"):
            print("FAIL: helper daemon is not running (native messaging broken?)")
            ok = False

        for url in args.expect_tunnel_url:
            got = egress_ip(wd, sess, url)
            verdict = "OK" if got == server else "FAIL"
            ok = ok and verdict == "OK"
            print(f"  tunnel-expected {url}: egress={got} (server={server}) {verdict}")

        for url in args.expect_direct_url:
            got = egress_ip(wd, sess, url)
            verdict = "OK" if got and got != server else "FAIL"
            ok = ok and verdict == "OK"
            print(f"  direct-expected {url}: egress={got} {verdict}")

        print("RESULT:", "OK" if ok else "FAIL")
        return 0 if ok else 1
    finally:
        gd.terminate()
        subprocess.run([BIN, "--stop"], env=env, capture_output=True)


if __name__ == "__main__":
    sys.exit(main())
