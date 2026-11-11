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
    # У удалялки другой набор требований: ей не нужны curl/certutil/сверка суммы,
    # зато нужно снять регистрацию, остановить хелпер и отодвинуть занятый exe.
    is_uninstall = "uninstall" in path.lower()
    # упоминание в комментарии («pure batch, no PowerShell») — не вызов
    calls = [ln for ln in text.splitlines()
             if not ln.strip().lower().startswith(("rem ", "rem\t", "::"))
             and re.search(r"(^|[&|(]\s*|\bcall\s+)powershell", ln, re.I)]
    check(not calls, f"нет вызовов powershell (найдено {len(calls)})")
    check(".ps1" not in low, "не запускает .ps1")
    if is_uninstall:
        check(re.search(r"reg\s+delete\s+.*hkcu", low) is not None, "снимает регистрацию в HKCU")
        check('--stop' in low, "останавливает хелпер перед удалением")
        check(re.search(r"move\s+/y\s+\"%target_exe%\"", text, re.I) is not None, "отодвигает занятый exe перед удалением")
        check("rmdir" in low, "удаляет каталог установки")
    else:
        check("curl.exe" in low, "скачивание через curl.exe (есть в Windows 10+)")
        check("certutil" in low, "sha256 через certutil -hashfile")
        check("sha256sums" in low, "сверяет с SHA256SUMS")
    # Манифест и ветку реестра пишет сам хелпер: cmd.exe кодирует текст кодовой
    # страницей консоли, и у пользователя с нелатинским именем в LOCALAPPDATA
    # Firefox не мог прочитать JSON. Поэтому проверяем ОБРАТНОЕ: что JSON не
    # собирается через echo и что вызывается --install-manifest.
    if not is_uninstall:
        check("--install-manifest" in low, "манифест пишет хелпер (--install-manifest)")
    builds_json = re.search(r'>\s*"%target_json%"\s*\(', text, re.I) is not None
    check(not builds_json, "JSON не собирается через echo (кодировка консоли)")

    # обновление поверх работающего exe: Windows не даёт записать в занятый файл,
    # поэтому установщик обязан остановить хелпер и/или отодвинуть .exe в сторону
    if not is_uninstall:
        check(re.search(r'"%target_exe%"\s+--stop', text, re.I) is not None, "останавливает хелпер перед заменой exe")
        check(re.search(r"move\s+/y\s+\"%target_exe%\"", text, re.I) is not None, "отодвигает занятый exe в .old перед копированием")

    uses_bang = re.search(r"![A-Za-z_][A-Za-z0-9_]*!", text) is not None
    has_expansion = "enabledelayedexpansion" in low
    if uses_bang:
        check(has_expansion, "есть setlocal enabledelayedexpansion (используются !VAR!)")

    # Путь в JSON экранировать больше не нужно: манифест пишет хелпер через
    # encoding/json. Проверяем, что ручной сборки пути в JSON не осталось.
    # именно ручная сборка JSON-пути (EXE_JSON с удвоенными слешами), а не
    # безобидная переменная TARGET_JSON с путём к файлу
    manual = re.search(r"set\s+\"?EXE_JSON|:\\\\=\\\\\\\\", text, re.I) is not None
    if manual:
        msg = "путь для JSON собирается вручную (должен писать хелпер)"
        print(f"  {BAD} {msg}")
        errors.append(msg)
    else:
        print(f"  {OK} путь в JSON не собирается вручную")

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
