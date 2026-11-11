#!/usr/bin/env python3
"""Тесты «как пользователь»: что делает живой человек с хелпером и интерфейсом.

Администратор вводит правильные значения. Человек вставляет адрес из браузера
целиком («https://vpn.ru/»), копирует порт в поле хоста, оставляет поле порта
пустым, пишет имя устройства эмодзи, жмёт кнопку дважды, закрывает попап в
середине операции. Хелпер обязан ответить внятным русским текстом за разумное
время — и никогда зависанием, трейсбеком или «ok:true» без результата.

Часть проверок про ввод в интерфейсе — в panel_ui_probe.js (там настоящий DOM).
Здесь: разбор ввода, тексты ошибок, устойчивость к мусору и жизненный цикл.
"""
import json
import os
import struct
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.environ.get("AWGBV_BIN", os.path.join(ROOT, "dist", "awgbv-linux-amd64"))
PEER_SCRIPT = os.path.join(ROOT, "install", "peer", "awgbv-peer.sh")
STATE = "/tmp/awgbv-naive-state"
SERVER = os.environ.get("AWGBV_TEST_SERVER", "10.10.100.2")

results = []


def nm(cmd, timeout=120, peer_script=True, **kw):
    """Кадр native messaging → (ответ, длительность)."""
    msg = {"id": 1, "cmd": cmd, **kw}
    raw = json.dumps(msg).encode()
    env = {**os.environ, "AWGBV_STATE_DIR": STATE}
    if peer_script:
        # иначе проверка спотыкается о скачивание скрипта из релиза, а не о ввод
        env["AWGBV_PEER_SCRIPT"] = PEER_SCRIPT
    started = time.time()
    try:
        proc = subprocess.run([BIN], input=struct.pack("<I", len(raw)) + raw,
                              capture_output=True, env=env, timeout=timeout)
    except subprocess.TimeoutExpired:
        return {"_timeout": True, "error": "нет ответа"}, time.time() - started
    took = time.time() - started
    out = proc.stdout
    if len(out) < 4:
        return {"_bad": True, "error": (proc.stderr or b"").decode()[:300]}, took
    size = struct.unpack("<I", out[:4])[0]
    try:
        return json.loads(out[4:4 + size]), took
    except Exception as exc:
        return {"_bad": True, "error": f"ответ не JSON: {exc}"}, took


def check(ok, label, detail=""):
    results.append((bool(ok), label, detail))
    print(("  OK   " if ok else "  FAIL ") + label + (f" — {detail}" if detail else ""))


INTERNAL = ("goroutine", "runtime error", "panic:", "/root/", ".go:", "Process exited",
            "exit status", "bad json", "unknown cmd")


def clean_error(reply):
    if reply.get("_timeout") or reply.get("_bad"):
        return False, "нет нормального ответа"
    if reply.get("ok"):
        return False, "ответил успехом"
    err = (reply.get("error") or "").strip()
    if not err:
        return False, "пустая ошибка"
    for bad in INTERNAL:
        if bad in err:
            return False, f"в тексте внутренности: {bad}"
    return True, err


def main():
    os.makedirs(STATE, exist_ok=True)
    for name in ("servers.json", "awgbv_access_key", "known_hosts"):
        src = f"/tmp/paneltest2/.local/state/awgbv/{name}"
        if os.path.exists(src) and not os.path.exists(os.path.join(STATE, name)):
            subprocess.run(["cp", src, os.path.join(STATE, name)], check=False)
    key = os.path.join(STATE, "awgbv_access_key")
    if os.path.exists(key):
        os.chmod(key, 0o600)

    print("== адрес сервера так, как его вставляют люди ==")
    # сервер известен и доступен: проверяем, что ввод приводится к рабочему виду
    cases = [
        ("с https:// и слэшем", "https://" + SERVER + "/", 0, True),
        ("с пробелами", "   " + SERVER + "   ", 0, True),
        ("с портом в поле хоста", SERVER + ":22", 0, True),
        ("с ssh:// и root@", "ssh://root@" + SERVER + ":22", 0, True),
        ("пустое поле порта", SERVER, "", True),
        ("порт строкой", SERVER, "22", True),
        ("порт null", SERVER, None, True),
    ]
    for label, host, port, should_work in cases:
        reply, took = nm("peer_selftest", host=host, sshPort=port)
        ok = bool(reply.get("ok")) if should_work else not reply.get("ok")
        detail = reply.get("error") or (reply.get("peers") or {}).get("container") or "ok"
        check(ok and took < 60, f"{label} → {'работает' if should_work else 'отказ'} за {took:.1f}с",
              str(detail)[:70])

    for label, host, port in (
        ("пустой адрес", "", 0),
        ("только пробелы", "   ", 0),
        ("несуществующий домен", "нет-такого-сервера.invalid", 0),
        ("порт словами", SERVER, "двадцать два"),
        ("порт 70000", SERVER, 70000),
        ("порт -1", SERVER, -1),
    ):
        reply, took = nm("peer_selftest", host=host, sshPort=port)
        ok, detail = clean_error(reply)
        check(ok and took < 90, f"{label} → внятный отказ за {took:.1f}с", detail[:80])
        if label in ("пустой адрес", "только пробелы"):
            # раньше здесь было «сервер :22 не принял логин или пароль»
            check("адрес" in detail.lower(), f"{label} → текст про адрес, а не про пароль", detail[:70])
        if label in ("порт 70000", "порт -1"):
            check("65535" in detail, f"{label} → текст про диапазон портов", detail[:70])

    print("\n== имя устройства: фантазия пользователя ==")
    names = [
        ("эмодзи", "Мой 📱 ноут"),
        ("60 символов", "о" * 60),
        ("пустое", ""),
        ("с кавычками", 'мой "домашний" ноут'),
        ("с точкой с запятой", "ноут; rm -rf /"),
        ("с подчёркиванием", "work_laptop"),
        ("по-китайски", "我的笔记本"),
        ("с переводом строки", "ноут\nвторой"),
    ]
    created = []
    for label, name in names:
        reply, took = nm("peer_create", host=SERVER, sshPort=22, name=name)
        if reply.get("ok"):
            peer = reply.get("peer") or {}
            created.append(peer.get("publicKey"))
            shown = (peer.get("name") or "").strip()
            # имя должно быть читаемым: ни пустого, ни с переводом строки
            ok = bool(shown) and "\n" not in shown
            check(ok, f"имя {label} → в списке «{shown}»", peer.get("allowedIps", ""))
        else:
            ok, detail = clean_error(reply)
            check(ok, f"имя {label} → внятный отказ", detail[:70])
    # уборка за собой: тестовые ключи не должны оставаться на сервере
    for pub in created:
        if pub:
            nm("peer_revoke", host=SERVER, sshPort=22, publicKey=pub)

    print("\n== ключи: что происходит при повторном отзыве ==")
    reply, took = nm("peer_create", host=SERVER, sshPort=22, name="naive-test")
    peer = reply.get("peer") or {}
    pub = peer.get("publicKey")
    check(bool(pub), "ключ выпущен для проверки отзыва", peer.get("allowedIps", ""))
    if pub:
        first, _ = nm("peer_revoke", host=SERVER, sshPort=22, publicKey=pub)
        check(bool(first.get("ok")), "первый отзыв прошёл")
        second, _ = nm("peer_revoke", host=SERVER, sshPort=22, publicKey=pub)
        ok, detail = clean_error(second)
        check(ok, "повторный отзыв того же ключа → понятный отказ", detail[:70])
        junk, _ = nm("peer_revoke", host=SERVER, sshPort=22, publicKey="AAAA")
        ok, detail = clean_error(junk)
        check(ok, "отзыв мусорного ключа → понятный отказ", detail[:70])

    print("\n== состояние: пользователь ещё ничего не настраивал ==")
    reply, _ = nm("status")
    check(bool(reply.get("ok")) and reply.get("state") in (None, "stopped", "idle"),
          "status на пустом состоянии не падает", f"state={reply.get('state')}")
    reply, _ = nm("peer_list", host="")
    ok, detail = clean_error(reply)
    check(ok, "список ключей без сервера → понятный отказ", detail[:70])
    reply, _ = nm("peer_servers")
    check(bool(reply.get("ok")), "реестр серверов читается",
          f"серверов: {len(reply.get('servers') or [])}")

    print("\n== проверка версий: пользователь ждёт «всё актуально» ==")
    reply, took = nm("update_check")
    upd = reply.get("update") or {}
    check(bool(upd.get("latest")), "проверка версий вернула данные",
          f"latest={upd.get('latest')} helper={upd.get('helperLatest')}")
    check(bool(reply.get("ok")) and not reply.get("error"),
          "проверка версий не показывает ошибку при наличии данных", str(reply.get("error"))[:60])

    print("\n== мусор и опечатки ==")
    for label, cmd in (("опечатка", "peer_lst"), ("пустая", ""), ("длинная", "x" * 5000)):
        reply, took = nm(cmd, host=SERVER)
        ok = not reply.get("_bad") and not reply.get("_timeout") and not reply.get("ok")
        check(ok and took < 20, f"команда {label} → отказ за {took:.1f}с",
              str(reply.get("error"))[:60])

    print("\n== «а если браузер закрылся во время работы» ==")
    # обрываем кадр на середине: хелпер должен пережить это и не оставить мусора
    env = {**os.environ, "AWGBV_STATE_DIR": STATE}
    proc = subprocess.Popen([BIN], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, env=env)
    proc.stdin.write(struct.pack("<I", 500) + b'{"id":1,"cmd":"status"}')
    proc.stdin.flush()
    proc.stdin.close()
    try:
        proc.wait(timeout=15)
        check(proc.returncode == 0, "оборванный кадр → хелпер завершился штатно",
              f"код {proc.returncode}")
    except subprocess.TimeoutExpired:
        proc.kill()
        check(False, "оборванный кадр → хелпер завис")

    # пустой ввод: браузер открыл порт и сразу закрыл
    proc = subprocess.run([BIN], input=b"", capture_output=True, env=env, timeout=20)
    check(proc.returncode == 0, "пустой ввод → штатный выход", f"код {proc.returncode}")

    failed = [r for r in results if not r[0]]
    print(f"\nитого: {len(results)} проверок, провалов: {len(failed)}")
    for _ok, label, detail in failed:
        print(f"  ✗ {label} — {detail}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
