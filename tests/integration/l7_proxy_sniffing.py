#!/usr/bin/env python3
"""Loopback-only end-to-end test for Xray HTTP Host and TLS SNI routing.

Run with:
  XRAY_BINARY=/path/to/xray XRAY_LOCATION_ASSET=/path/to/assets \
    python3 tests/integration/l7_proxy_sniffing.py

The script starts one temporary Xray process and a loopback-only target server.
It never changes host routes, firewall rules, DNS or system services.
"""
from __future__ import annotations

import json
import os
import signal
import socket
import ssl
import struct
import subprocess
import tempfile
import threading
import time
from pathlib import Path


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_for_count(hits: list[float], expected: int, timeout: float = 2.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if len(hits) >= expected:
            return True
        time.sleep(0.025)
    return len(hits) >= expected


def socks_connect(proxy_port: int, target_port: int) -> socket.socket | None:
    sock = socket.create_connection(("127.0.0.1", proxy_port), timeout=3)
    sock.settimeout(2)
    sock.sendall(b"\x05\x01\x00")
    if sock.recv(2) != b"\x05\x00":
        sock.close()
        raise RuntimeError("SOCKS5 greeting negotiation failed")
    request = b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", target_port)
    sock.sendall(request)
    reply = sock.recv(10)
    if len(reply) < 2:
        sock.close()
        raise RuntimeError("SOCKS5 CONNECT reply was truncated")
    if reply[1] != 0:
        sock.close()
        return None
    return sock


def exercise_http(proxy_port: int, target_port: int, host: str) -> None:
    sock = socks_connect(proxy_port, target_port)
    if sock is None:
        return
    try:
        request = f"GET / HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n"
        sock.sendall(request.encode("ascii"))
        time.sleep(0.15)
    except OSError:
        pass
    finally:
        sock.close()


def exercise_tls(proxy_port: int, target_port: int, host: str) -> None:
    sock = socks_connect(proxy_port, target_port)
    if sock is None:
        return
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    try:
        wrapped = context.wrap_socket(sock, server_hostname=host, do_handshake_on_connect=False)
        wrapped.settimeout(1)
        wrapped.do_handshake()
    except (OSError, ssl.SSLError):
        pass
    finally:
        try:
            sock.close()
        except OSError:
            pass


def main() -> int:
    xray = os.environ.get("XRAY_BINARY", "").strip()
    asset_dir = os.environ.get("XRAY_LOCATION_ASSET", "").strip()
    if not xray or not asset_dir:
        raise SystemExit("Set XRAY_BINARY and XRAY_LOCATION_ASSET")
    if not Path(xray).is_file() or not os.access(xray, os.X_OK):
        raise SystemExit("XRAY_BINARY must point to an executable file")
    if not Path(asset_dir).is_dir():
        raise SystemExit("XRAY_LOCATION_ASSET must point to a directory")

    proxy_port, target_port = free_port(), free_port()
    hits: list[float] = []
    stop = threading.Event()
    target = socket.socket()
    target.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    target.bind(("127.0.0.1", target_port))
    target.listen(16)
    target.settimeout(0.2)

    def target_loop() -> None:
        while not stop.is_set():
            try:
                conn, _ = target.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            hits.append(time.monotonic())
            conn.close()

    target_thread = threading.Thread(target=target_loop, daemon=True)
    target_thread.start()

    config = {
        "log": {"loglevel": "warning"},
        "inbounds": [{
            "tag": "l7-e2e-socks",
            "listen": "127.0.0.1",
            "port": proxy_port,
            "protocol": "socks",
            "settings": {"auth": "noauth", "udp": True},
            "sniffing": {
                "enabled": True,
                "destOverride": ["http", "tls"],
                "metadataOnly": False,
                "routeOnly": True,
            },
        }],
        "outbounds": [
            {"tag": "direct", "protocol": "freedom"},
            {"tag": "block", "protocol": "blackhole"},
        ],
        "routing": {
            "rules": [{
                "type": "field",
                "inboundTag": ["l7-e2e-socks"],
                "domain": ["domain:blocked.example"],
                "outboundTag": "block",
            }]
        },
    }

    fd, config_path = tempfile.mkstemp(prefix="hrcore-l7-e2e-", suffix=".json", dir="/tmp")
    os.close(fd)
    with open(config_path, "w", encoding="utf-8") as stream:
        json.dump(config, stream)

    process = subprocess.Popen(
        [xray, "run", "-config", config_path],
        env={**os.environ, "XRAY_LOCATION_ASSET": asset_dir},
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )

    def wait_proxy() -> None:
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if process.poll() is not None:
                output = process.stdout.read() if process.stdout else ""
                raise RuntimeError(f"Xray exited before accepting SOCKS: {output}")
            try:
                with socket.create_connection(("127.0.0.1", proxy_port), timeout=0.2):
                    return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("Xray SOCKS inbound did not start")

    def assert_blocked_then_direct(label: str, exercise) -> None:
        before = len(hits)
        exercise(proxy_port, target_port, "blocked.example")
        time.sleep(0.25)
        if len(hits) != before:
            raise AssertionError(f"{label}: blocked.example unexpectedly reached the direct target")
        exercise(proxy_port, target_port, "allowed.example")
        if not wait_for_count(hits, before + 1):
            raise AssertionError(f"{label}: allowed.example did not reach the direct target")

    try:
        wait_proxy()
        assert_blocked_then_direct("HTTP Host", exercise_http)
        assert_blocked_then_direct("TLS SNI", exercise_tls)
        print("L7_PROXY_SNIFFING_E2E_OK: HTTP Host and TLS SNI rules route blocked.example to block and allowed.example to direct")
        return 0
    finally:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)
        stop.set()
        target.close()
        target_thread.join(timeout=1)
        try:
            os.unlink(config_path)
        except FileNotFoundError:
            pass


if __name__ == "__main__":
    raise SystemExit(main())
