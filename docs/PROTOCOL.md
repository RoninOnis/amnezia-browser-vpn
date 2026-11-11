# Протоколы

## 1. Native messaging (расширение ↔ хелпер)

Firefox запускает `awgbv` без аргументов (режим native messaging host) и общается кадрами:
`uint32 LE length` + UTF-8 JSON.

Запрос:

```json
{ "id": 7, "cmd": "connect", "profile": { ... }, "listen": "127.0.0.1:0" }
```

Команды: `ping`, `version`, `status`, `connect`, `disconnect`, `quit`.

Ответ:

```json
{ "id": 7, "ok": true, "error": "", "status": { "running": true, "state": "connected",
  "socksPort": 45678, "lastHandshakeUnix": 1800000000, "rxBytes": 92, "txBytes": 557 } }
```

`state`: `stopped` | `connecting` (устройство поднято, handshake ещё не было) | `connected` |
`error`.

## 2. Control-протокол (хелпер ↔ демон)

Первый запуск `awgbv` создаёт демон (`awgbv --daemon`, отдельная сессия через `setsid` /
`DETACHED_PROCESS`) и общается с ним по loopback TCP. Координаты демона:

* `control.json` в каталоге состояния (`0600`): `{ "port": N, "token": "...", "pid": N }`
* запрос — одна строка JSON, обязательное поле `token`
* одна строка JSON в ответ

Демон держит максимум один туннель; `connect` перезапускает предыдущий. Файл-замок
(`daemon.lock`, flock на Linux / exclusive-create на Windows) не даёт поднять два демона.

## 3. Формат `vpn://`

```
vpn:// + base64url( uint32be(len(uncompressed)) || zlib(json) )
```

JSON верхнего уровня — описание контейнеров сервера:

```json
{
  "containers": [
    { "container": "amnezia-awg2",
      "awg": { "Jc": "6", "S1": "144", "H1": "…", "I1": "<r 2><b 0x…>",
               "port": "40000",
               "last_config": "{\"client_priv_key\":\"…\",\"server_pub_key\":\"…\", …}" } }
  ],
  "dns1": "1.1.1.1", "dns2": "1.0.0.1",
  "hostName": "203.0.113.10", "description": "my-server"
}
```

Клиентские параметры (ключи, `client_ip`, `mtu`, `persistent_keep_alive`, порт) лежат в
`containers[].awg.last_config` **как вложенная JSON-строка**. Расширение и хелпер разбирают это
независимо друг от друга: JS — через `DecompressionStream('deflate')`, Go — через `compress/zlib`.
