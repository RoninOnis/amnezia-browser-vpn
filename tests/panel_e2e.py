#!/usr/bin/env python3
"""Живой прогон панели администратора через настоящий интерфейс расширения.

Зачем отдельно от browser_e2e.py: тот проверяет туннель и раздельное
туннелирование, панель в нём не участвует. Юнит-тесты гоняют renderAdmin на
заглушке DOM и не поймают, что кнопка в живом попапе ничего не делает (не тот
селектор, перекрытый обработчик, гонка с опросом каждые 4 секунды).

Как устроено: Firefox WebDriver НЕ пускает в привилегированный контекст
moz-extension://, поэтому драйвер (tests/panel_ui_probe.js) внедряется в копию
расширения и работает внутри самого попапа: жмёт настоящие кнопки, ждёт
состояние и отправляет результат на локальный HTTP-сервер этого скрипта.
Firefox запускается напрямую, без geckodriver.

Проверяется против ЖИВОГО сервера:
  1. вкладка «Панель» открывается, сервер и список ключей приходят из хелпера;
  2. «Новый ключ» выпускает пир, ссылка vpn:// видна в карточке;
  3. «Добавить в мои серверы» добавляет профиль, повторный клик не плодит дубликат;
  4. «Отозвать» убирает ключ с сервера и из списка;
  5. выпущенный профиль реально подключается (egress = IP сервера).

Запуск (сервер уже настроен панелью: нужны servers.json и ключ доступа):
    python3 tests/panel_e2e.py --state /tmp/paneltest2/.local/state/awgbv \
        --expect-ip 213.165.34.238
"""
import argparse
import http.server
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXT = os.path.join(ROOT, "extension")
TESTEXT = "/tmp/awgbv-panel-ext"
PROFILE = "/tmp/awgbv-panel-ffprofile"
NM_DIR = os.path.expanduser("~/.mozilla/native-messaging-hosts")
STATE = "/tmp/awgbv-panel-state"
ADDON_ID = "awgbv@0xoni.ru"
PORT = 8791

BIN = os.environ.get("AWGBV_BIN", os.path.join(ROOT, "dist", "awgbv-linux-amd64"))
FF = os.environ.get("FF_BIN", "/opt/fftest/firefox/firefox")


class Collector:
    """Принимает отчёт тест-драйвера из попапа."""

    def __init__(self):
        self.payload = None
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(length)
                try:
                    outer.payload = json.loads(body.decode("utf-8"))
                except Exception as exc:
                    outer.payload = {"error": f"bad json: {exc}", "steps": []}
                self.send_response(204)
                self.end_headers()

            def log_message(self, *args):
                pass

        self.httpd = http.server.HTTPServer(("127.0.0.1", PORT), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def start(self):
        self.thread.start()

    def stop(self):
        self.httpd.shutdown()


def build_extension(port):
    shutil.rmtree(TESTEXT, ignore_errors=True)
    shutil.copytree(EXT, TESTEXT)

    bg = os.path.join(TESTEXT, "background.js")
    src = open(bg, encoding="utf-8").read()
    defaults = {"enabled": False, "autoConnect": False, "settingsVersion": 2,
                "profiles": [], "activeProfileId": "", "whitelist": [], "blacklist": []}
    src, n = re.subn(r"const DEFAULTS = \{.*?\n\};",
                     "const DEFAULTS = " + json.dumps(defaults, ensure_ascii=False) + ";",
                     src, flags=re.S)
    if not n:
        raise RuntimeError("не нашёл блок DEFAULTS в background.js")
    # попап открывает сам фон: так экран живёт в настоящем origin расширения
    src += "\nbrowser.tabs.create({ url: browser.runtime.getURL('popup.html') });\n"
    open(bg, "w", encoding="utf-8").write(src)

    probe = open(os.path.join(ROOT, "tests", "panel_ui_probe.js"), encoding="utf-8").read()
    open(os.path.join(TESTEXT, "test-driver.js"), "w", encoding="utf-8").write(
        probe.replace("__PORT__", str(port)))

    popup = os.path.join(TESTEXT, "popup.html")
    html = open(popup, encoding="utf-8").read()
    if "</body>" not in html:
        raise RuntimeError("в popup.html нет </body>")
    html = html.replace("</body>", '  <script src="test-driver.js"></script>\n</body>')
    open(popup, "w", encoding="utf-8").write(html)

    xpi = "/tmp/awgbv-panel.xpi"
    with zipfile.ZipFile(xpi, "w", zipfile.ZIP_DEFLATED) as z:
        for root, _d, files in os.walk(TESTEXT):
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
        json.dump({"name": "awgbv", "description": "Amnezia Browser VPN helper",
                   "path": BIN, "type": "stdio", "allowed_extensions": [ADDON_ID]}, f, indent=2)


def seed_state(src_state):
    shutil.rmtree(STATE, ignore_errors=True)
    os.makedirs(STATE)
    for name in ("awgbv_access_key", "servers.json", "known_hosts"):
        src = os.path.join(src_state, name)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(STATE, name))
    os.chmod(os.path.join(STATE, "awgbv_access_key"), 0o600)


def revoke_via_helper(public_key, host):
    """Отзывает пира тем же путём, что и панель: кадр native messaging → хелпер."""
    import struct
    msg = {"id": 1, "cmd": "peer_revoke", "host": host, "publicKey": public_key}
    raw = json.dumps(msg).encode()
    env = {**os.environ, "AWGBV_STATE_DIR": STATE}
    proc = subprocess.run([BIN], input=struct.pack("<I", len(raw)) + raw,
                          capture_output=True, env=env, timeout=180)
    if len(proc.stdout) < 4:
        return False, proc.stderr.decode()[:200]
    size = struct.unpack("<I", proc.stdout[:4])[0]
    reply = json.loads(proc.stdout[4:4 + size])
    return bool(reply.get("ok")), reply.get("error", "")


def panel_host():
    path = os.path.join(STATE, "servers.json")
    servers = json.load(open(path)) if os.path.exists(path) else []
    return servers[0]["host"] if servers else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--state", required=True, help="каталог состояния хелпера с servers.json")
    ap.add_argument("--expect-ip", default="", help="IP сервера для проверки выпущенного профиля")
    ap.add_argument("--timeout", type=int, default=300)
    args = ap.parse_args()

    xpi = build_extension(PORT)
    prepare_profile(xpi)
    install_native_manifest()
    seed_state(args.state)
    env = {**os.environ, "AWGBV_STATE_DIR": STATE}
    subprocess.run([BIN, "--stop"], env=env, capture_output=True)

    collector = Collector()
    collector.start()
    ff = subprocess.Popen([FF, "-headless", "-no-remote", "-profile", PROFILE],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
    ok = True
    try:
        deadline = time.time() + args.timeout
        while time.time() < deadline and collector.payload is None:
            time.sleep(1)
        payload = collector.payload
        if payload is None:
            print("FAIL: попап не отчитался за отведённое время")
            return 1

        for step in payload.get("steps", []):
            mark = "OK  " if step.get("ok") else "FAIL"
            ok = ok and bool(step.get("ok"))
            extra = f" — {step['extra']}" if step.get("extra") else ""
            print(f"  {mark} {step.get('label')}{extra}")
        if payload.get("error"):
            print("  ошибка драйвера:", payload["error"][:400])
            ok = False

        link = payload.get("link") or ""
        if not link.startswith("vpn://"):
            print("FAIL: ссылка не выпущена")
            ok = False
        else:
            # ссылка обязана разбираться хелпером — иначе она бесполезна
            dump = subprocess.run([BIN, "--dump-profile", "/dev/stdin"], input=link,
                                  capture_output=True, text=True, env=env)
            parsed = dump.returncode == 0
            print(("  OK  " if parsed else "FAIL") + " выпущенная ссылка разбирается хелпером")
            ok = ok and parsed

        if args.expect_ip:
            subprocess.run([BIN, "--stop"], env=env, capture_output=True)
            subprocess.run([BIN, "--connect", link], env=env, capture_output=True, timeout=120)
            st = {}
            for _ in range(30):
                out = subprocess.run([BIN, "--status"], capture_output=True, text=True, env=env).stdout
                st = json.loads(out or "{}")
                if st.get("state") == "connected":
                    break
                time.sleep(1)
            print(("  OK  " if st.get("state") == "connected" else "FAIL")
                  + f" туннель поднялся профилем панели (state={st.get('state')})")
            ok = ok and st.get("state") == "connected"
            port = st.get("socksPort")
            if port:
                got = subprocess.run(["curl", "-s", "--max-time", "25", "--socks5-hostname",
                                      f"127.0.0.1:{port}", "https://api.ipify.org"],
                                     capture_output=True, text=True).stdout.strip()
                hit = got == args.expect_ip
                print(("  OK  " if hit else "FAIL") + f" трафик идёт через сервер (egress={got})")
                ok = ok and hit

        # уборка: выпущенный ключ отзываем, чтобы на сервере не оставалось сирот
        key_pub = payload.get("keyPublicKey") or ""
        if key_pub:
            subprocess.run([BIN, "--stop"], env=env, capture_output=True)
            revoked, err = revoke_via_helper(key_pub, panel_host())
            print(("  OK  " if revoked else "FAIL") + " тестовый ключ отозван после проверки"
                  + (f" ({err})" if err else ""))

        print("RESULT:", "OK" if ok else "FAIL")
        return 0 if ok else 1
    finally:
        collector.stop()
        ff.send_signal(signal.SIGTERM)
        try:
            ff.wait(timeout=15)
        except subprocess.TimeoutExpired:
            ff.kill()
        subprocess.run([BIN, "--stop"], env=env, capture_output=True)


if __name__ == "__main__":
    sys.exit(main())
