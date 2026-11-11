# Amnezia Browser VPN (Firefox)

Туннель Amnezia только для браузера: через ваш сервер AmneziaWG идёт лишь Firefox,
остальная система — мимо туннеля. Разделение делается по именам хостов, системные
маршруты не меняются.

> English: a Firefox extension plus a small Go helper. The helper runs AmneziaWG in user
> space (no TUN, no root) and exposes a loopback SOCKS5 proxy; the extension decides per
> request whether to use it. AmneziaWG obfuscation (Jc/Jmin/Jmax, S1–S4, H1–H4, I1–I5) is
> fully supported.
>
> «Amnezia» в названии — только указание на совместимость с профилями `vpn://` и
> протоколом AmneziaWG. Проект независимый, не связан с командой amnezia-vpn.

## Как это работает

```
Firefox + расширение (MV3)
  proxy.onRequest: домен → SOCKS5 | DIRECT
        │ native messaging (JSON по stdio)
awgbv (Go, без root и TUN): amneziawg-go + gVisor netstack → SOCKS5 на 127.0.0.1
        │ UDP (обфусцированный AmneziaWG)
ваш сервер Amnezia (amnezia-awg / awg2)
```

- Расширение не может поднять VPN само: у WebExtensions нет UDP и криптостека, максимум — `browser.proxy`.
- Туннель целиком в user space: без TUN, без прав администратора, без правок маршрутов.
- Разделение трафика — в браузере, а не по `AllowedIPs`: запрос либо уходит в SOCKS5, либо идёт напрямую.
- Помощник работает демоном: переживает выгрузку фоновой страницы и перезапуск браузера.
- «Включено» означает «работать всегда»: после перезапуска туннель поднимается сам, сторож проверяет
  состояние раз в минуту, паузы между попытками 15 → 30 → 60 с → до 5 минут.
- Подбор MTU: если handshake не пришёл за 8 с, помощник спускается 1376 → 1280 → 1240 → 1200.
  Нужно в сетях, где рядом работает другой VPN (AnyConnect держит MTU 1400).

## Установка

**1. Помощник** — один файл, без клонирования репозитория и без прав администратора:

```bash
bash install-linux.sh          # Linux: определит архитектуру, сверит sha256, пропишет манифест
```

```bat
install-windows.cmd            :: Windows: чистый batch, curl.exe + certutil + reg add в HKCU
```

Оба файла лежат в ассетах релиза. Ставит в `~/.local/bin/awgbv` (Linux) или
`%LOCALAPPDATA%\AwgBrowserVpn\awgbv.exe` (Windows), плюс манифест native messaging
(внутрь snap-песочницы, если Firefox — snap).

**2. Расширение** — подписанный `.xpi` из [releases](https://github.com/RoninOnis/amnezia-browser-vpn/releases):
`about:addons` → шестерёнка → «Установить дополнение из файла». Подписано на AMO по каналу
**unlisted**: в каталоге не публикуется, но ставится как обычное дополнение и переживает перезапуск.

**3. Ключ** — в приложении Amnezia «Поделиться» → скопировать ссылку `vpn://…`; в расширении
раздел **Серверы** → вставить → «Добавить профиль». Поддерживается только AmneziaWG
(`amnezia-awg`, `amnezia-awg2`); XRay, OpenVPN и Cloak — нет.

**Из исходников:** `./install/build.sh` (Go 1.25+, бинари в `dist/`), `./install/linux/install.sh
dist/awgbv-linux-amd64`, `./install/sign.sh` для подписи в AMO. ID дополнения `awgbv@0xoni.ru`
фиксирован — от него зависит native messaging.

## Интерфейс

Попап 420×600 с нижней навигацией. Палитра из `AmneziaStyle.qml` (`#101012`, `#18181b`, `#1f1f23`,
акцент `#f59e0b`).

| Раздел | Что внутри |
|---|---|
| **Главная** | кнопка подключения, состояние, трафик и аптайм, текущий сервер, переключатель для открытой вкладки |
| **Серверы** | профили, выбор активного, добавление по `vpn://`; на первом запуске — «С чего начать» |
| **Сайты** | две вкладки-списка: «Через VPN» и «Напрямую» |
| **Панель** | ключи и служба на **своём** сервере (SSH); помечена «необязательно» |
| **Настройки** | автоподключение, подбор MTU, локальный порт туннеля, состояние и версия помощника, «последняя попытка» |

В интерфейсе вспомогательная программа называется **«помощник»** (в коде — `helper`).

## Разделение трафика

Отдельного переключателя режима нет — режим выводится из содержимого списков.

| Состояние списков | Что попадает в туннель |
|---|---|
| «Через VPN» пуст | всё, **кроме** адресов из «Напрямую» |
| «Через VPN» не пуст | **только** его адреса (минус всё, что в «Напрямую») |

Записи: `example.com` (с поддоменами), `*.example.com`, `.example.com`, IP. Адрес не может быть в
двух списках: при добавлении он уходит из противоположного, «Напрямую» всегда приоритетнее.
На «Главной» есть переключатель для открытой вкладки: **Авто / Через VPN / Напрямую** — действует
поверх списков, сбрасывается при закрытии вкладки. `localhost`, `127.0.0.0/8`, `::1`, `*.local`
в туннель не отправляются никогда.

## Панель администратора

Управление своим сервером AmneziaWG из попапа: список устройств, выпуск ключа, отзыв, установка
обходчика на 443.

- **HTTP API у клиента Amnezia нет** — управление идёт по SSH. После первичной настройки помощник
  подключается пользователем `awgbv`, чей ключ ограничен
  `restrict,command="sudo /usr/local/sbin/awgbv-peer"`: этим ключом можно только `list` / `add` / `remove`.
- Приватный ключ устройства создаётся у пользователя (X25519) и на сервер не уходит — только
  публичный ключ, PSK и внутренний адрес.
- Контейнер не перезапускается: изменения применяются `awg syncconf`, иначе у остальных клиентов
  рвалась бы сессия.
- Запись атомарная, с бэкапом `.bak`, проверкой после применения и полным откатом при сбое.
- Пароль root нужен один раз для первичной настройки и не сохраняется. Хост-ключ сервера
  запоминается при первом подключении (TOFU), при расхождении подключение отменяется.

Серверная часть — `install/peer/awgbv-peer.sh` (наша реализация, MIT). Установка вручную:

```bash
sudo bash install-peer.sh --pubkey "ssh-ed25519 AAAA... awgbv"
```

Проверка логики без сервера: `bash install/peer/tests/test-peer-script.sh` (36 проверок).

## Сети, где разрешён только UDP 443

Корпоративные сети часто выпускают наружу UDP только на 443 (там QUIC). Лечится «спутником» на том
же хосте, где стоит AmneziaWG. Сначала посмотреть план, ничего не меняя:

```bash
curl -fsSL -o install-satellite.sh \
  https://github.com/RoninOnis/amnezia-browser-vpn/releases/latest/download/install-satellite.sh
sudo bash install-satellite.sh --dry-run      # печатает найденные контейнеры и план
sudo bash install-satellite.sh                # установка
```

Спутник слушает `443/udp` и передаёт пакеты локальному AWG. Не меняется: контейнер Amnezia не
пересоздаётся и не перезапускается, ключи не читаются, ссылки `vpn://` остаются прежними, клиенту
ничего вписывать не нужно — помощник сам пробует 443, если штатный порт молчит.

Цель задаётся вручную, если автоопределение не подошло:
`sudo bash install-satellite.sh --target 172.29.172.2:37689`.

Две ловушки: порт нельзя брать из `docker port` (легко поймать чужой контейнер вроде
`amnezia-telemt`) — скрипт спрашивает `ListenPort` у самого AWG; релей на отдельном хосте
бесполезен из-за hairpin NAT — только тот же хост, что и AWG.

Удаление:

```bash
sudo systemctl disable --now awgbv-satellite
sudo rm -f /etc/awgbv-satellite.env /etc/systemd/system/awgbv-satellite.service /usr/local/bin/awgbv-satellite
```

## Обновление помощника

```bash
awgbv --self-update      # скачать последний релиз и подменить бинарь
awgbv --version          # что стоит сейчас
```

В попапе это кнопка «Обновить помощника» в блоке «Обновления» — появляется, когда установленная
версия старее последнего релиза. Порядок: версия из `releases/latest` (кэш 6 часов) → сверка sha256
с `SHA256SUMS` → скачанный бинарь проверяется `--version` до подмены → старый файл остаётся как
`.old`, при неудаче возвращается на место. Остаться без VPN из-за обновления нельзя.

## Диагностика

```bash
awgbv --status
awgbv --foreground --mtu-auto --connect ./profile.conf   # обычная попытка
awgbv --foreground --port 443 --connect ./profile.conf   # только 443, проверка спутника
```

В логе видно, что происходит на самом деле:

```
tunnel up: 10.8.1.14 -> 203.0.113.10:36853, mtu 1376, socks5 127.0.0.1:33939
handshake ok after 501ms (mtu 1376, endpoint 203.0.113.10:36853)   # всё в порядке

no handshake on port 36853 mtu 1376 in 4s, next attempt            # штатный порт молчит
trying port 443 mtu 1376 (попытка 2 из 2)                          # проверяем спутник
tunnel works with port 443 mtu 1376 (profile had port 36853 mtu 1376)

connect: no handshake on port 443 mtu 1240 (UDP may be blocked)     # не проходит вообще
```

Со стороны сервера: `systemctl status awgbv-satellite`, `journalctl -u awgbv-satellite -n 20`
(`relay: сессия 203.0.113.50:51820 -> 172.29.172.2:37689 (прозрачно)`).

## Если что-то не работает

| Симптом | Причина и лечение |
|---|---|
| В попапе «No such native application awgbv» | Манифеста нет там, где его ищет Firefox. Обычно: установщик запускали через **sudo** (уехал в `/root`) или snap-Firefox (манифест и бинарь должны быть в `~/snap/firefox/common/`). Проверка: `ls -l ~/.mozilla/native-messaging-hosts/ ~/snap/firefox/common/.mozilla/native-messaging-hosts/` |
| Туннель подключён, но IP не меняется | Расширению не выдан доступ «Access your data for all websites»: без host permissions `proxy.onRequest` не срабатывает молча |
| Сайт в списке, но идёт напрямую | Запись в «Напрямую» перебивает «Через VPN». Быстрая проверка — переключатель «Этот сайт» на «Главной» |
| `state: connecting` дольше ~10 с | Сервер не отвечает на handshake: проверьте, что он жив и ключ не отозван. Если рядом корпоративный VPN — почти наверняка наружу пускают только UDP 443, нужен спутник |
| В сети с корпоративным VPN не поднимается совсем | `ping` ничего не значит: AnyConnect глушит ICMP, но открывает UDP 443. Проверка: `awgbv --foreground --port 443 --connect ./profile.conf` |
| Всё отвалилось после перезагрузки | `awgbv --status`, затем `awgbv --connect <conf>`. При включённом автоподключении расширение поднимет туннель само (раз в минуту) |
| Windows: `curl.exe not found` | Нужен Windows 10 1803+. На более старых скачайте `awgbv-windows-amd64.exe` и `SHA256SUMS` вручную в `%LOCALAPPDATA%\AwgBrowserVpn` |

## Ограничения

Это SOCKS5-прокси, а не системный VPN:

- **WebRTC может раскрыть реальный IP.** Если критично: `media.peerconnection.enabled = false` или
  `media.peerconnection.ice.proxy_only = true`.
- **HTTP/3 через SOCKS не используется** — весь браузерный трафик остаётся в TCP-туннеле.
- **DoH:** при включённом DNS-over-HTTPS имя резолвит DoH-провайдер, а не сервер Amnezia. Нужен
  серверный DNS — `network.trr.mode = 0`.
- **Только TCP:** UDP-приложения браузера (кроме QUIC, который отключается сам) в туннель не пойдут.
- **Ключи лежат в `storage.local` расширения** в открытом виде, как и в клиенте Amnezia. Кому это
  важно — отдельный профиль Firefox.

## Где что лежит

| Что | Linux | Windows |
|---|---|---|
| бинарь | `~/.local/bin/awgbv` | `%LOCALAPPDATA%\AwgBrowserVpn\awgbv.exe` |
| состояние, control-сокет | `~/.local/state/awgbv/` | `%LOCALAPPDATA%\AwgBrowserVpn\` |
| лог | `~/.local/state/awgbv/helper.log` | `%LOCALAPPDATA%\AwgBrowserVpn\helper.log` |

Управляющий порт и SOCKS5 слушают только `127.0.0.1`, доступ по одноразовому токену из
`control.json` (права `0600`).

## Разработка

```bash
cd helper && go test ./...                  # парсер vpn://, IPC, конфиги, лестница MTU, релей
node --test extension/tests/*.test.mjs      # расширение на заглушке браузера: списки, MTU, панель, первый запуск
python3 tests/e2e_nm.py dist/awgbv-linux-amd64 ./my.conf --expect-ip 203.0.113.10
```

Живые прогоны (нужны Firefox Developer Edition + geckodriver): `tests/browser_e2e.py` — туннель и
раздельное туннелирование, `tests/panel_e2e.py` — панель администратора в настоящем попапе,
`tests/naive_user.py` — поведение на неаккуратном вводе.

## Лицензия и название

Код проекта — MIT. Внутри: [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) (MIT),
[netstack из wireguard-go](https://github.com/WireGuard/wireguard-go) (MIT),
[go-socks5](https://github.com/things-go/go-socks5) (MIT).

Проект независимый: не связан с Amnezia VPN, не спонсируется и не одобряется её командой.
Слово «Amnezia» стоит только как указание на совместимость с профилями `vpn://` и протоколом
AmneziaWG на вашем сервере. Иконка — знак Amnezia (принадлежит проекту Amnezia, исходники под
GPL-3.0) в градиенте Firefox; прав на знак не заявляем и заменим по первому требованию
правообладателя через [issues](https://github.com/RoninOnis/amnezia-browser-vpn/issues).
