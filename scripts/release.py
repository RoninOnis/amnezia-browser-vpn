#!/usr/bin/env python3
"""Публикация релиза awgbv на GitHub.

    python3 scripts/release.py --version 0.3.7 --check     # только проверить ассеты
    python3 scripts/release.py --version 0.3.7 --publish   # создать релиз и залить

Скрипт намеренно требует явного --publish: подпись в AMO и публикация — действия,
которые нельзя отменить (номер версии сжигается), поэтому «случайно» их не делаем.

Ассеты, без которых фича не работает у пользователя:
  * подписанный .xpi — сам аддон;
  * install-linux.sh / install-windows.cmd — установка хелпера одной командой;
  * бинари хелпера и SHA256SUMS — их сверяет установщик;
  * awgbv-peer.sh — серверная часть панели администратора (её скачивает хелпер);
  * install-satellite.sh / install-relay.sh — установщики спутника и релея.

SHA256SUMS расширяется хешами скриптов: и панель, и установщик спутника сверяют
скачанный скрипт по этому файлу, иначе проверка молча пропускается.
"""
import argparse
import glob
import hashlib
import json
import os
import shutil
import subprocess
import sys
import urllib.request

REPO = "RoninOnis/amnezia-browser-vpn"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STAGE = "/tmp/awgbv-release"


def token():
    path = os.path.expanduser("~/.hermes/github_token_roninoni")
    with open(path) as fh:
        return fh.read().strip()


def api(path, data=None, method=None, upload=False):
    host = "uploads.github.com" if upload else "api.github.com"
    url = f"https://{host}{path}"
    body = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(url, data=body, method=method or ("POST" if body else "GET"))
    req.add_header("Authorization", f"Bearer {token()}")
    req.add_header("Accept", "application/vnd.github+json")
    if body:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read() or b"{}")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def collect(version):
    """Собирает площадку релиза и возвращает список предупреждений."""
    warn = []
    if os.path.isdir(STAGE):
        shutil.rmtree(STAGE)
    os.makedirs(STAGE)

    xpis = sorted(glob.glob(f"{ROOT}/dist/signed/*{version}*.xpi"))
    if not xpis:
        sys.exit(f"error: нет подписанного .xpi версии {version} в dist/signed/")
    shutil.copy(xpis[-1], f"{STAGE}/amnezia-browser-vpn-{version}.xpi")

    copy = {
        f"{ROOT}/install/linux/install-remote.sh": "install-linux.sh",
        f"{ROOT}/install/windows/install-remote.cmd": "install-windows.cmd",
        f"{ROOT}/install/peer/awgbv-peer.sh": "awgbv-peer.sh",
        f"{ROOT}/install/peer/install-peer.sh": "install-peer.sh",
        f"{ROOT}/install/relay/install-satellite.sh": "install-satellite.sh",
        f"{ROOT}/install/relay/install-relay.sh": "install-relay.sh",
    }
    for src, name in copy.items():
        if not os.path.exists(src):
            warn.append(f"нет файла {src} — ассет {name} не попадёт в релиз")
            continue
        shutil.copy(src, f"{STAGE}/{name}")

    binaries = glob.glob(f"{ROOT}/dist/awgbv-*")
    if not binaries:
        sys.exit("error: нет собранных бинарей в dist/ — сначала VERSION=x ./install/build.sh")
    for path in binaries:
        shutil.copy(path, f"{STAGE}/{os.path.basename(path)}")
    if not any("windows" in b for b in binaries):
        warn.append("в dist/ нет windows-бинаря")

    # SHA256SUMS: бинари + скрипты, иначе проверки на стороне пользователя молча пропускаются
    lines = []
    for name in sorted(os.listdir(STAGE)):
        if name.endswith((".xpi", ".json")):
            continue
        lines.append(f"{sha256(os.path.join(STAGE, name))}  {name}")
    with open(f"{STAGE}/SHA256SUMS", "w") as fh:
        fh.write("\n".join(lines) + "\n")
    return warn


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--version", required=True)
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--publish", action="store_true")
    ap.add_argument("--notes-file")
    args = ap.parse_args()
    if not (args.check or args.publish):
        sys.exit("укажите --check или --publish")

    warn = collect(args.version)
    print(f"площадка: {STAGE}")
    for name in sorted(os.listdir(STAGE)):
        size = os.path.getsize(os.path.join(STAGE, name))
        print(f"  {name}  {size // 1024}K")
    for w in warn:
        print("! " + w)

    # Проверка, что версия в бинаре и в манифесте совпадает с релизной
    manifest = json.load(open(f"{ROOT}/extension/manifest.json"))
    if manifest.get("version") != args.version:
        sys.exit(f"error: в manifest.json версия {manifest.get('version')}, а релиз {args.version}")
    for path in glob.glob(f"{STAGE}/awgbv-linux-amd64"):
        out = subprocess.run([path, "--version"], capture_output=True, text=True).stdout.strip()
        print(f"  {os.path.basename(path)} --version → {out}")
        if out != args.version:
            sys.exit(f"error: бинарь сообщает {out}, а релиз {args.version}")

    if args.check:
        print("\n--check: ничего не публикую")
        return

    tag = f"v{args.version}"
    name = f"Amnezia Browser VPN {args.version}"
    notes = open(args.notes_file).read() if args.notes_file else f"Релиз {args.version}."

    for rel in api(f"/repos/{REPO}/releases"):
        if rel["tag_name"] == tag:
            sys.exit(f"error: релиз {tag} уже существует — номер не переиспользуем")

    rel = api(f"/repos/{REPO}/releases",
              {"tag_name": tag, "name": name, "body": notes, "draft": False, "prerelease": False},
              method="POST")
    print("релиз:", rel["id"], rel["html_url"])

    for fname in sorted(os.listdir(STAGE)):
        path = os.path.join(STAGE, fname)
        with open(path, "rb") as fh:
            data = fh.read()
        req = urllib.request.Request(f"https://uploads.github.com/repos/{REPO}/releases/{rel['id']}/assets?name={fname}",
                                     data=data, method="POST")
        req.add_header("Authorization", f"Bearer {token()}")
        req.add_header("Content-Type", "application/octet-stream")
        with urllib.request.urlopen(req) as resp:
            info = json.loads(resp.read())
        print(f"  загружен {fname} ({info['size'] // 1024}K)")

    assets = api(f"/repos/{REPO}/releases/{rel['id']}/assets")
    print(f"\nитого ассетов: {len(assets)}")
    for a in assets:
        print(f"  {a['name']:<34} {a['size'] // 1024}K  {a.get('digest', '')}")


if __name__ == "__main__":
    main()
