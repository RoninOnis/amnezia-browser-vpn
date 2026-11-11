#!/usr/bin/env python3
"""End-to-end check of the native-messaging helper + SOCKS5 tunnel.

It exercises the same path the extension uses:
  spawn `awgbv` (native-messaging mode) -> frame JSON over stdin/stdout ->
  connect with a real profile -> speak SOCKS5 to the returned port ->
  do a TLS request through the tunnel -> disconnect -> quit.

Usage:
    python3 tests/e2e_nm.py <path-to-awgbv-binary> <vpn:// link | conf file> [--expect-ip 1.2.3.4]

No third-party modules required.
"""
import json
import os
import socket
import ssl
import struct
import subprocess
import sys
import time


def frame(payload: dict) -> bytes:
    body = json.dumps(payload).encode()
    return struct.pack("<I", len(body)) + body


def read_frame(stream) -> dict:
    hdr = stream.read(4)
    if len(hdr) < 4:
        raise RuntimeError("helper closed the pipe")
    (n,) = struct.unpack("<I", hdr)
    body = stream.read(n)
    if len(body) < n:
        raise RuntimeError("short frame")
    return json.loads(body)


class Helper:
    def __init__(self, binary: str, env=None):
        self.proc = subprocess.Popen(
            [binary],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
        )
        self.seq = 0

    def call(self, cmd: str, **extra) -> dict:
        self.seq += 1
        self.proc.stdin.write(frame({"id": self.seq, "cmd": cmd, **extra}))
        self.proc.stdin.flush()
        return read_frame(self.proc.stdout)

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=5)
        except Exception:
            self.proc.kill()


def load_profile(binary: str, arg: str) -> dict:
    out = subprocess.run([binary, "--dump-profile", arg], capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError("dump-profile failed: " + out.stderr.strip())
    return json.loads(out.stdout)


def socks5_connect(port: int, host: str, dport: int, timeout: float = 15.0) -> socket.socket:
    s = socket.create_connection(("127.0.0.1", port), timeout=timeout)
    s.sendall(b"\x05\x01\x00")
    if s.recv(2) != b"\x05\x00":
        raise RuntimeError("socks5: method negotiation failed")
    req = b"\x05\x01\x00\x03" + bytes([len(host)]) + host.encode() + struct.pack(">H", dport)
    s.sendall(req)
    head = s.recv(4)
    if len(head) < 4 or head[0] != 5 or head[1] != 0:
        raise RuntimeError("socks5: connect failed: %r" % (head,))
    atyp = head[3]
    if atyp == 1:
        s.recv(4)
    elif atyp == 3:
        n = s.recv(1)[0]
        s.recv(n)
    elif atyp == 4:
        s.recv(16)
    s.recv(2)
    return s


def https_get_via_socks(port: int, host: str, path: str = "/") -> str:
    raw = socks5_connect(port, host, 443)
    ctx = ssl.create_default_context()
    tls = ctx.wrap_socket(raw, server_hostname=host)
    tls.sendall(f"GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n".encode())
    data = b""
    while True:
        chunk = tls.recv(4096)
        if not chunk:
            break
        data += chunk
    tls.close()
    return data.decode("utf-8", "replace")


def main() -> int:
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    binary, prof_arg = sys.argv[1], sys.argv[2]
    expect_ip = None
    if "--expect-ip" in sys.argv:
        expect_ip = sys.argv[sys.argv.index("--expect-ip") + 1]

    env = dict(os.environ)
    env.setdefault("AWGBV_STATE_DIR", "/tmp/awgbv-e2e-state")
    profile = load_profile(binary, prof_arg)
    print(f"profile: {profile['name']} -> {profile['host']}:{profile['port']} ({profile.get('container')})")

    h = Helper(binary, env=env)
    try:
        ver = h.call("version")
        assert ver["ok"], ver
        print("helper version:", ver.get("version"))

        st = h.call("status")
        assert st["ok"], st
        print("initial status:", st["status"]["state"])
        assert not st["status"]["running"], "daemon should not be running yet"

        t0 = time.time()
        res = h.call("connect", profile=profile, listen="127.0.0.1:0")
        if not res["ok"]:
            print("connect failed:", res.get("error"))
            return 1
        st = res["status"]
        port = st["socksPort"]
        print(f"tunnel up in {time.time()-t0:.1f}s: socks5 127.0.0.1:{port} state={st['state']}")

        # Wait for a handshake before judging the tunnel.
        for _ in range(30):
            st = h.call("status")["status"]
            if st.get("lastHandshakeUnix"):
                break
            time.sleep(1)
        print(f"handshake: {st.get('lastHandshakeUnix')} rx={st.get('rxBytes')} tx={st.get('txBytes')}")

        body = https_get_via_socks(port, "api.ipify.org", "/")
        payload = body.split("\r\n\r\n", 1)[1].strip()
        print("egress IP through tunnel:", payload)
        if expect_ip and payload != expect_ip:
            print(f"MISMATCH: expected {expect_ip}")
            return 1

        res = h.call("disconnect")
        assert res["ok"], res
        print("after disconnect:", res["status"]["state"])
        res = h.call("quit")
        print("quit:", res["ok"])
        return 0
    finally:
        h.close()


if __name__ == "__main__":
    sys.exit(main())
