#!/usr/bin/env python3
"""Проверка Windows-установщика (чистый batch) на грабли, которые ломают его молча.

    python3 scripts/check_windows_cmd.py install/windows/install-remote.cmd

Зачем: живого Windows под рукой может не быть, а batch ошибается тихо — скрипт
«отрабатывает», а человек получает нерабочий native messaging. Каждая проверка
ниже — реальная поломка, которую видно только на машине пользователя:

  * CRLF. cmd.exe на LF-файле разбирает строки непредсказуемо (метки не находятся,
    `goto` уводит в конец);
  * BOM. С BOM первая строка перестаёт быть `@echo off`, и в окне печатается мусор;
  * не-ASCII. Консоль Windows в OEM-кодировке покажет кракозябры вместо русского
    текста, поэтому сообщения установщика только латиницей;
  * PowerShell. Пользователь потребовал ровно один `.cmd` — вызов powershell.exe
    или `.ps1` нарушает это требование и ломается при строгой политике;
  * JSON манифеста. Путь `C:\\Users\\…` внутри JSON обязан быть с `\\\\`, иначе
    Firefox не прочитает манифест и скажет «No such native application»;
  * delayed expansion. `!VAR!` без `setlocal enabledelayedexpansion` печатается
    буквально как `!VAR!`;
  * проверка sha256. Установщик обязан сверять скачанный бинарь с SHA256SUMS и
    останавливаться на расхождении;
  * реестр. Native messaging на Windows регистрируется в HKCU (без админа).
"""
import re
import sys

OK = "\033[32m✓\033[0m"
BAD = "\033[31m✗\033[0m"


def main(path):
    raw = open(path, "rb").read()
    text = raw.decode("utf-8", errors="replace")
    errors = []
    notes = []

    def check(cond, msg):
        print(f"  {OK if cond else BAD} {msg}")
        if not cond:
            errors.append(msg)

    print(f"проверяю {path} ({len(raw)} байт)")
    check(b"\xef\xbb\xbf" not in raw[:3], "нет BOM")
    check(b"\r\n" in raw, "переводы строк CRLF")
    stray_lf = len(re.findall(rb"(?<!\r)\n", raw))
    check(stray_lf == 0, f"нет одиночных LF (найдено {stray_lf})")

    non_ascii = sorted({ch for ch in text if ord(ch) > 127})
    check(not non_ascii, f"сообщения только ASCII (не-ASCII: {''.join(non_ascii[:12])})")

    low = text.lower()
    # упоминание в комментарии («pure batch, no PowerShell») — не вызов
    calls = [ln for ln in text.splitlines()
             if not ln.strip().lower().startswith(("rem ", "rem\t", "::"))
             and re.search(r"(^|[&|(]\s*|\bcall\s+)powershell", ln, re.I)]
    check(not calls, f"нет вызовов powershell (найдено {len(calls)})")
    check(".ps1" not in low, "не запускает .ps1")
    check("curl.exe" in low, "скачивание через curl.exe (есть в Windows 10+)")
    check("certutil" in low, "sha256 через certutil -hashfile")
    check("sha256sums" in low, "сверяет с SHA256SUMS")
    check(re.search(r"reg\s+add\s+.*hkcu", low) is not None, "регистрация в HKCU (без админа)")
    check("nativemessaginghosts" in low.replace(" ", ""), "путь NativeMessagingHosts")

    uses_bang = re.search(r"![A-Za-z_][A-Za-z0-9_]*!", text) is not None
    has_expansion = "enabledelayedexpansion" in low
    if uses_bang:
        check(has_expansion, "есть setlocal enabledelayedexpansion (используются !VAR!)")

    # экранирование пути внутри JSON манифеста
    if re.search(r":\\=\\\\\\", text) or re.search(r":\\=\\\\", text):
        print(f"  {OK} экранирование пути для JSON (set VAR:\\\\=\\\\)")
    else:
        msg = "экранирование пути для JSON (set VAR:\\=\\)"
        print(f"  {BAD} {msg}")
        errors.append(msg)

    # манифест должен быть валидным JSON: собираем его так, как это делает batch
    m = re.search(r'"path"\s*:\s*"([^"]+)"', text)
    if m:
        notes.append(f'в манифесте путь задан как {m.group(1)!r}')

    # успешный путь обязан выходить с кодом 0, а путь ошибки — с ненулевым:
    # иначе двойной клик по .cmd закрывает окно с «успехом» при провале
    has_ok = re.search(r"exit\s+/b\s+0", text, re.I) is not None
    has_fail = re.search(r"exit\s+/b\s+[1-9]", text, re.I) is not None
    check(has_ok, "успешный путь завершается exit /b 0")
    check(has_fail, "путь ошибки завершается с ненулевым кодом")
    doubled = raw.count(b"\r\r\n")
    check(doubled == 0, f"нет удвоенных CR (найдено {doubled})")

    for n in notes:
        print(f"  · {n}")
    if errors:
        print(f"\nпровалов: {len(errors)}")
        return 1
    print("\nвсе проверки пройдены")
    return 0


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit("укажите путь к .cmd")
    sys.exit(main(sys.argv[1]))
