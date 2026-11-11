# Amnezia Browser VPN (Firefox)

Browser-only [Amnezia](https://github.com/amnezia-vpn/amnezia-client) tunnel: route **only Firefox**
through your own AmneziaWG server, with per-site black/white lists — the rest of the machine stays on
the normal connection.

> English summary: a Firefox extension + a small Go helper. The helper runs AmneziaWG in user space
> (no TUN device, no root) and exposes a loopback SOCKS5 proxy; the extension decides per request
> whether to use that proxy or go direct. AmneziaWG obfuscation (Jc/Jmin/Jmax, S1–S4, H1–H4, I1–I5)
> is fully supported.
>
> Names and marks: "Amnezia" is used nominatively only, to state compatibility with `vpn://` profiles
> created by the Amnezia app and with the AmneziaWG protocol. This project is independent — not
> affiliated with, sponsored by, or endorsed by the amnezia-vpn team. The icon combines the Amnezia
> mark (owned by the Amnezia project, sources under GPL-3.0) with the Firefox gradient; no rights to
> the mark are claimed and we will remove or replace it on request.

---

## Зачем

Десктопный клиент Amnezia заворачивает **всю** систему. Если нужен VPN только в браузере
(остальное — локальная сеть, банки, игры, RDP без задержек), обычный split tunneling в Amnezia
приходится настраивать по IP-подсетям, что для сайтов неудобно. Здесь разделение делается по
**именам хостов**, прямо в браузере, и системный маршрут по умолчанию не меняется вообще.

## Как это работает

```
┌─ Firefox ──────────────────────────────────┐
│ расширение (MV3)                            │
│  • импорт ключа vpn://                      │
│  • белые / чёрные списки по доменам         │
│  • proxy.onRequest: домен → SOCKS5 | DIRECT │
└───────────────┬────────────────────────────┘
                │ native messaging (JSON по stdio)
┌───────────────▼────────────────────────────┐
│ awgbv (Go, один бинарь, без root и TUN)     │
│  amneziawg-go (userspace)                   │
│  + gVisor netstack → SOCKS5 на 127.0.0.1    │
│  демон: живёт отдельно от браузера          │
└───────────────┬────────────────────────────┘
                │ UDP (обфусцированный AmneziaWG)
        ваш Amnezia-сервер (amnezia-awg / awg2)
```

Ключевые решения:

* **Расширение не может само поднять VPN.** У WebExtensions нет доступа к UDP и нет криптостека —
  максимум, что оно умеет, это `browser.proxy`. Поэтому нужен локальный компаньон.
* **Никакого TUN и прав администратора.** Туннель целиком в user space: `amneziawg-go` +
  gVisor netstack. Одинаково работает на Windows и Linux, ничего не устанавливает в ядро и не
  трогает маршруты.
* **Разделение трафика — на уровне браузера, а не по `AllowedIPs`.** Запрос либо уходит в SOCKS5
  (и только там попадает в туннель), либо идёт напрямую и в туннель не попадает вообще. Поэтому нет
  «тихих» дропов пакетов вне `AllowedIPs`.
* **DNS.** В туннельных запросах имя резолвится на стороне сервера (`proxyDNS: true`), локальный
  резолвер не участвует. В direct-запросах — как обычно, локально.
* **Хелпер — демон.** Он переживает выгрузку background-страницы расширения и перезапуск браузера;
  расширение при старте просто переподключается к нему.
* **Автозапуск и самовосстановление.** «Включено» означает «хочу, чтобы работало всегда»: после
  перезапуска Firefox и после падения демона расширение поднимает туннель само. Сторож проверяет
  состояние раз в минуту; если сервер недоступен, пауза между попытками растёт
  15 с → 30 с → 60 с → до 5 мин, чтобы не заваливать сервер handshake'ами. Явное включение
  кнопкой сбрасывает паузу. Тумблер в настройках («Подключаться при запуске браузера») по
  умолчанию включён; выключить его можно, но тогда после перезапуска браузера туннель ждать не надо.
* **Подбор MTU.** Если в системе работает другой VPN (Cisco AnyConnect держит туннель с MTU 1400),
  наши пакеты при MTU профиля 1376 молча теряются. Хелпер при подключении ждёт первый handshake
  8 секунд и, если его нет, спускается вниз: 1376 → 1280 → 1240 → 1200. В настройках видно
  фактический MTU и пометку «подобран под сеть»; подбор можно выключить тумблером.
* **Одно нажатие — одна попытка.** Первый `connect` после старта может попасть в момент, когда
  демон ещё поднимается, поэтому расширение повторяет попытку через 1,5 с, а хелпер дожидается
  устойчивого состояния туннеля (до 12 с) вместо мгновенного «connecting». Раньше из-за этого
  приходилось нажимать «подключить» дважды.

## Диагностика

Хелпер умеет проверять подключение без расширения:

```bash
awgbv --foreground --mtu-auto --connect ./profile.conf
```

В логе видно, что происходит на самом деле:

```
tunnel up: 10.8.1.14 -> 203.0.113.10:36853, mtu 1376, socks5 127.0.0.1:33939
handshake ok after 501ms (mtu 1376, endpoint 203.0.113.10:36853)      # всё в порядке
no handshake with mtu 1376 in 8s, trying lower                        # MTU велик или UDP режется
connect: no handshake with mtu 1200 (UDP may be blocked)              # UDP не проходит вообще
```

`--mtu 1280` задаёт MTU вручную. Строка `handshake ok` пишется один раз при подключении —
именно её отсутствие раньше сбивало диагностику.

## Установка

### 1. Собрать хелпер

Нужен Go 1.25+.

```bash
./install/build.sh            # → dist/awgbv-linux-amd64, awgbv-windows-amd64.exe, +arm64, SHA256SUMS
```

### 2. Установить хелпер (native messaging)

**Самый быстрый путь — один файл, без клонирования репозитория.** Скрипт сам определит
архитектуру, скачает бинарь из релиза, сверит sha256 и пропишет native-messaging манифест
(включая snap-песочницу Firefox). Root не нужен.

```bash
bash install-linux.sh                          # Linux (берёт последний релиз)
```

```bat
install-windows.cmd
```

Один файл, чистый batch: `curl.exe` скачивает нужный бинарь, `certutil` сверяет sha256 с
`SHA256SUMS`, `copy` кладёт его в `%LOCALAPPDATA%\AwgBrowserVpn`, `reg add` регистрирует манифест
в `HKCU`. PowerShell не используется вообще — поэтому политика выполнения не мешает.

Оба скрипта лежат в ассетах релиза. Ниже — установка из репозитория.

**Linux**

```bash
./install/linux/install.sh dist/awgbv-linux-amd64
```

Ставит бинарь в `~/.local/bin/awgbv` и манифест в `~/.mozilla/native-messaging-hosts/awgbv.json`
(плюс внутрь snap-песочницы, если Firefox — snap).

**Windows** (без прав администратора)

```powershell
powershell -ExecutionPolicy Bypass -File install\windows\install.ps1 -Binary dist\awgbv-windows-amd64.exe
```

Ставит `%LOCALAPPDATA%\AwgBrowserVpn\awgbv.exe`, рядом манифест и ключ
`HKCU\Software\Mozilla\NativeMessagingHosts\awgbv`.

### 3. Подключить расширение

**Постоянно (рекомендуется).** Возьмите подписанный `.xpi` из
[releases](https://github.com/RoninOnis/amnezia-browser-vpn/releases) и поставьте его:
`about:addons` → шестерёнка → «Установить дополнение из файла» → выбрать `.xpi`.
Дополнение подписано на addons.mozilla.org по каналу **unlisted** (self-distribution):
оно не публикуется в каталоге AMO и не ищется в нём, ставится только файлом, но
release-сборка Firefox принимает его как обычное дополнение — оно переживает перезапуск.

**Для разработки.** `about:debugging` → «Это Firefox» → «Загрузить временное дополнение» →
`extension/manifest.json` (живёт до перезапуска браузера).

**Пересобрать и подписать самому:**

```bash
./install/build.sh                       # бинари хелпера
./install/sign.sh                        # → dist/signed/<slug>-<version>.xpi
```

`install/sign.sh` берёт ключ из `AMO_API_KEY`/`AMO_API_SECRET` либо из
`~/.hermes/amo_creds.yaml` (см. шапку скрипта). ID дополнения фиксирован —
`awgbv@0xoni.ru`: от него зависит native messaging, менять нельзя.

### 4. Добавить ключ

В приложении Amnezia: у подключения «Поделиться» → скопировать ссылку `vpn://…`.
В расширении: раздел **Серверы** → вставить ссылку → «Добавить профиль».
Поддерживается только AmneziaWG (`amnezia-awg`, `amnezia-awg2`); XRay/OpenVPN/Cloak не
поддерживаются — под них нужен другой транспорт, а не SOCKS5.

## Интерфейс

Попап — одно окно 420×600 с нижней навигацией, как в мобильном Amnezia. Палитра взята из
`AmneziaStyle.qml` (`#101012`, `#18181b`, `#1f1f23`, акцент `#f59e0b`).

| Раздел | Что внутри |
|---|---|
| **Главная** | большая кнопка подключения, состояние, трафик и аптайм, текущий сервер, переключатель для открытого сайта |
| **Серверы** | список профилей, выбор активного, добавление по `vpn://` |
| **Сайты** | две вкладки-списка: «Через VPN» и «Напрямую» |
| **Настройки** | автоподключение, порт SOCKS5, состояние хелпера, «последняя попытка» с причиной, версия |

## Разделение трафика

Списки — как в десктопном клиенте: две вкладки, **отдельного переключателя режима нет**,
режим выводится из содержимого.

| Состояние списков | Что попадает в туннель |
|---|---|
| список **«Через VPN» пуст** | всё, **кроме** адресов из списка «Напрямую» |
| список **«Через VPN» не пуст** | **только** его адреса (минус всё, что есть в «Напрямую») |

Записи: `example.com` (включая поддомены), `*.example.com`, `.example.com`, IP. Один и тот же
адрес не может быть в двух списках: при добавлении он уходит из противоположного, а список
«Напрямую» всегда приоритетнее.

На «Главной» есть быстрый переключатель для открытой вкладки: **Авто / Через VPN / Напрямую** —
он действует поверх списков и сбрасывается при закрытии вкладки.

`localhost`, `127.0.0.0/8`, `::1`, `*.local` никогда не отправляются в туннель.

## Проверка без браузера

```bash
awgbv --status
awgbv --connect config.conf --listen 127.0.0.1:1081    # или vpn:// ссылка, или JSON профиля
curl --socks5-hostname 127.0.0.1:1081 https://api.ipify.org   # должен вернуть IP сервера
awgbv --stop
```

Полный e2e-прогон (native messaging + SOCKS5 + TLS через туннель):

```bash
python3 tests/e2e_nm.py dist/awgbv-linux-amd64 ./my.conf --expect-ip 203.0.113.10
```

Проверка в реальном браузере (нужны Firefox Developer Edition + geckodriver, потому что
release-сборка не грузит неподписанное расширение):

```bash
FF_BIN=/path/to/firefox GECKODRIVER=/path/to/geckodriver \
python3 tests/browser_e2e.py ./my.conf --mode whitelist --whitelist ipinfo.io \
  --expect-tunnel-url https://ipinfo.io/ip --expect-direct-url https://api.ipify.org
# → ipinfo.io отвечает IP сервера, api.ipify.org — локальным IP
```

## Тесты

```bash
cd helper && go test ./...                          # парсер vpn://, IPC, конфиги
node --test extension/tests/split.test.mjs          # логика списков и proxy.onRequest
```

## Где что лежит

| Что | Linux | Windows |
|---|---|---|
| бинарь | `~/.local/bin/awgbv` | `%LOCALAPPDATA%\AwgBrowserVpn\awgbv.exe` |
| состояние, control-сокет | `~/.local/state/awgbv/` | `%LOCALAPPDATA%\AwgBrowserVpn\` |
| лог хелпера | `~/.local/state/awgbv/helper.log` | `%LOCALAPPDATA%\AwgBrowserVpn\helper.log` |

Демон слушает управляющий порт только на `127.0.0.1`, доступ — по одноразовому токену из
`control.json` (права `0600`). SOCKS5 тоже только loopback.

## Если что-то не работает

| Симптом | Причина и лечение |
|---|---|
| Windows: `Амперсанд (&) не разрешен` | Старый баг установщика (`.ps1` в UTF-8 без BOM, PowerShell 5.1 читал как ANSI). Обновиться на релиз `v0.3.0+`: там только `install-windows.cmd`, PowerShell внутри не используется. |
| Windows: `curl.exe not found` | Установщик требует Windows 10 1803+ (там curl встроен). На более старых системах скачайте `awgbv-windows-amd64.exe` и `SHA256SUMS` вручную и положите exe в `%LOCALAPPDATA%\AwgBrowserVpn`. |
| В попапе «No such native application awgbv» | Манифеста нет там, где его ищет Firefox, или он указывает на недоступный путь. Типичные причины: (1) установщик запускали через **sudo** — манифест уехал в `/root`, а Firefox ищет в домашнем каталоге пользователя; (2) **snap-Firefox** — и манифест, и бинарь должны лежать внутри `~/snap/firefox/common/`; установщик делает это сам, но только если запущен без sudo. Проверка: `ls -l ~/snap/firefox/common/.mozilla/native-messaging-hosts/ ~/.mozilla/native-messaging-hosts/`. |
| Туннель подключён, но IP не меняется | Расширению не выдан доступ «Access your data for all websites». Без host permissions фильтр `proxy.onRequest` не срабатывает **молча** — это документированное требование API. Проверьте `about:addons` → разрешения. |
| Сайт попал в список, но идёт напрямую | Проверьте, в каком списке он лежит: запись в «Напрямую» перебивает «Через VPN». Быстрая проверка — переключатель «Этот сайт» на «Главной». |
| `state: connecting` дольше ~10 c | Сервер не отвечает на handshake: проверьте UDP-порт в `vpn://` (не блокирует ли провайдер/файрвол), что сервер жив и ключ не отозван. |
| Всё отвалилось после перезагрузки | Демон не поднялся: `awgbv --status`, затем `awgbv --connect <conf>`. При включённом автоподключении расширение поднимет туннель само при следующем тике (раз в минуту). |
| Нужно посмотреть, что решило расширение | `about:debugging` → «Inspect» у расширения → консоль: каждое решение видно в `proxy.onError` и в статусах демона. Лог хелпера: `~/.local/state/awgbv/helper.log`. |

## Ограничения и как их закрывать

Это SOCKS5-прокси, а не «настоящий» системный VPN, поэтому:

* **WebRTC может раскрыть реальный IP** (ICE-кандидаты не всегда идут через прокси). Если это
  критично: `media.peerconnection.enabled = false` в `about:config` либо
  `media.peerconnection.ice.proxy_only = true`.
* **HTTP/3 (QUIC) через SOCKS не используется** — Firefox при активном прокси ходит по HTTP/2.
  Это скорее плюс: весь браузерный трафик остаётся в TCP-туннеле.
* **DoH.** Если включён DNS-over-HTTPS, запросы идут к DoH-провайдеру через прокси (то есть в
  туннель), но имя резолвит он, а не сервер Amnezia. Если нужен именно серверный DNS —
  `network.trr.mode = 0`.
* **Только TCP-прокси.** UDP-приложения браузера (кроме QUIC, который отключается сам) в туннель не
  пойдут.
* **Ключи хранятся в `storage.local` расширения** в открытом виде — как и в десктопном клиенте
  Amnezia. Кому это важно, ставить расширение в отдельный профиль Firefox.

## Название, знак и совместимость

Проект независимый. Он не связан с Amnezia VPN, не аффилирован с командой
[amnezia-vpn](https://github.com/amnezia-vpn), не спонсируется и не одобряется ею. Расширение
написано с нуля; из чужого кода в нём — только библиотека
[amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go), подключаемая как обычная зависимость
по её собственной лицензии (MIT), и знак для иконки (о нём ниже).

Слово «Amnezia» в названии и описании стоит **исключительно как указание на совместимость**: этот
инструмент работает с профилями `vpn://`, которые создаёт приложение Amnezia, и с протоколом
AmneziaWG на вашем собственном сервере. Это не заявка на бренд и не попытка выдать проект за
официальный клиент. Задача ровно обратная — чтобы те, у кого Amnezia уже есть, могли пользоваться
своим сервером ещё и в браузере, не заворачивая в туннель всю систему.

Иконка составная и читается буквально: **знак Amnezia** (принадлежит проекту Amnezia, исходники
под GPL-3.0) в **градиенте Firefox** — «туннель Amnezia, но только для браузера». Прав на знак
проект не заявляет. Если правообладатель захочет, чтобы мы перестали его использовать или
заменили на нейтральный, — достаточно написать в
[issues](https://github.com/RoninOnis/amnezia-browser-vpn/issues), вопрос закроем сразу.

## Лицензия

Код проекта — MIT. Использует [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT),
[netstack из wireguard-go](https://github.com/WireGuard/wireguard-go) (MIT) и
[go-socks5](https://github.com/things-go/go-socks5) (MIT).
