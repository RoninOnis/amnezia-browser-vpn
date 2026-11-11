# Amnezia Browser VPN (Firefox)

Browser-only [Amnezia](https://github.com/amnezia-vpn/amnezia-client) tunnel: route **only Firefox**
through your own AmneziaWG server, with per-site black/white lists — the rest of the machine stays on
the normal connection.

> English summary: a Firefox extension + a small Go helper. The helper runs AmneziaWG in user space
> (no TUN device, no root) and exposes a loopback SOCKS5 proxy; the extension decides per request
> whether to use that proxy or go direct. AmneziaWG obfuscation (Jc/Jmin/Jmax, S1–S4, H1–H4, I1–I5)
> is fully supported.

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

## Установка

### 1. Собрать хелпер

Нужен Go 1.25+.

```bash
./install/build.sh            # → dist/awgbv-linux-amd64, awgbv-windows-amd64.exe, +arm64, SHA256SUMS
```

### 2. Установить хелпер (native messaging)

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

* Для разработки: `about:debugging` → «Это Firefox» → «Загрузить временное дополнение» →
  `extension/manifest.json` (временное дополнение живёт до перезапуска браузера).
* Постоянно: собрать `.xpi` и подписать его на AMO (unlisted). ID фиксирован —
  `awgbv@0xoni.ru`, от него зависит native messaging.

### 4. Добавить ключ

В приложении Amnezia: у подключения «Поделиться» → скопировать ссылку `vpn://…`.
В расширении: «Настройки» → «Добавить ключ vpn://…» → «Добавить профиль».
Поддерживается только AmneziaWG (`amnezia-awg`, `amnezia-awg2`); XRay/OpenVPN/Cloak не
поддерживаются — под них нужен другой транспорт, а не SOCKS5.

## Разделение трафика

Два режима, как в десктопном клиенте:

| Режим | Что попадает в туннель |
|---|---|
| **Чёрный список** (по умолчанию) | всё, **кроме** перечисленных доменов |
| **Белый список** | **только** перечисленные домены |

Записи: `example.com` (включая поддомены), `*.example.com`, `.example.com`, IP.
В попапе есть быстрый переключатель для текущей вкладки: **Авто / Через VPN / Напрямую** —
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
| В попапе «хелпер не найден» | Нет нативного манифеста или он указывает на несуществующий бинарь. Проверьте `~/.mozilla/native-messaging-hosts/awgbv.json` (Linux) / ключ `HKCU\Software\Mozilla\NativeMessagingHosts\awgbv` (Windows) и путь внутри. |
| Туннель подключён, но IP не меняется | Расширению не выдан доступ «Access your data for all websites». Без host permissions фильтр `proxy.onRequest` не срабатывает **молча** — это документированное требование API. Проверьте `about:addons` → разрешения. |
| Сайт попал в список, но идёт напрямую | Проверьте режим: в режиме чёрного списка запись означает «напрямую». Быстрая проверка — переключатель «Этот сайт» в попапе. |
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

## Лицензия

MIT. Использует [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT),
[netstack из wireguard-go](https://github.com/WireGuard/wireguard-go) (MIT) и
[go-socks5](https://github.com/things-go/go-socks5) (MIT).
