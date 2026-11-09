#!/usr/bin/env python3
"""Loopback-only QUIC SNI routing test over a SOCKS5 UDP ASSOCIATE tunnel.

Requires aioquic (see requirements-quic.txt), XRAY_BINARY and XRAY_LOCATION_ASSET.
The test starts a temporary Xray process and a local QUIC target. It never
changes host routes, firewall rules, DNS or system services.
"""
from __future__ import annotations

import asyncio
import ipaddress
import json
import os
import shutil
import signal
import socket
import struct
import subprocess
import tempfile
from pathlib import Path

from aioquic.asyncio import connect, serve
from aioquic.asyncio.protocol import QuicConnectionProtocol
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.events import HandshakeCompleted

HANDSHAKES: list[str | None] = []
EXPECTED_BLOCKED = False


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class ServerProtocol(QuicConnectionProtocol):
    def quic_event_received(self, event) -> None:
        if isinstance(event, HandshakeCompleted):
            HANDSHAKES.append(event.alpn_protocol)


class SocksUDPAssociation:
    def __init__(self, host: str, port: int) -> None:
        self.control = socket.create_connection((host, port), timeout=3)
        self.control.settimeout(3)
        self.control.sendall(b"\x05\x01\x00")
        if self._recv_exact(2) != b"\x05\x00":
            raise RuntimeError("SOCKS5 no-auth negotiation failed")
        self.control.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
        head = self._recv_exact(4)
        if head[0] != 5 or head[1] != 0:
            raise RuntimeError(f"SOCKS5 UDP ASSOCIATE failed: {head!r}")
        atyp = head[3]
        if atyp == 1:
            relay_host = socket.inet_ntoa(self._recv_exact(4))
        elif atyp == 4:
            relay_host = socket.inet_ntop(socket.AF_INET6, self._recv_exact(16))
        elif atyp == 3:
            length = self._recv_exact(1)[0]
            relay_host = self._recv_exact(length).decode()
        else:
            raise RuntimeError(f"Unsupported SOCKS relay ATYP {atyp}")
        relay_port = struct.unpack("!H", self._recv_exact(2))[0]
        if relay_host in ("0.0.0.0", "::", ""):
            relay_host = "127.0.0.1"
        if relay_port == 0:
            raise RuntimeError("SOCKS UDP relay returned port 0")
        self.relay = (relay_host, relay_port)

    def _recv_exact(self, size: int) -> bytes:
        data = b""
        while len(data) < size:
            part = self.control.recv(size - len(data))
            if not part:
                raise RuntimeError("SOCKS control connection closed")
            data += part
        return data

    def close(self) -> None:
        try:
            self.control.close()
        except OSError:
            pass


class SocksDatagramTransport:
    def __init__(self, association: SocksUDPAssociation) -> None:
        self.association = association
        self.base = None

    def sendto(self, data: bytes, addr=None) -> None:
        if addr is None:
            raise RuntimeError("QUIC datagram has no destination")
        host = ipaddress.ip_address(addr[0].split("%", 1)[0])
        if host.version == 6 and host.ipv4_mapped:
            host = host.ipv4_mapped
        port = int(addr[1])
        if host.version == 4:
            header = b"\x00\x00\x00\x01" + host.packed + struct.pack("!H", port)
        else:
            header = b"\x00\x00\x00\x04" + host.packed + struct.pack("!H", port)
        self.base.sendto(header + data)

    def close(self) -> None:
        if self.base is not None:
            self.base.close()
        self.association.close()

    def __getattr__(self, name):
        if self.base is None:
            raise AttributeError(name)
        return getattr(self.base, name)


class SocksDatagramProtocol(asyncio.DatagramProtocol):
    def __init__(self, inner, adapter: SocksDatagramTransport) -> None:
        self.inner = inner
        self.adapter = adapter

    def connection_made(self, transport) -> None:
        self.adapter.base = transport
        self.inner.connection_made(self.adapter)

    def datagram_received(self, data: bytes, addr) -> None:
        if len(data) < 4 or data[:3] != b"\x00\x00\x00":
            return
        atyp = data[3]
        offset = 4
        if atyp == 1:
            if len(data) < offset + 6:
                return
            host = socket.inet_ntoa(data[offset:offset + 4])
            offset += 4
        elif atyp == 4:
            if len(data) < offset + 18:
                return
            host = socket.inet_ntop(socket.AF_INET6, data[offset:offset + 16])
            offset += 16
        elif atyp == 3:
            if len(data) < offset + 1:
                return
            length = data[offset]
            offset += 1
            if len(data) < offset + length + 2:
                return
            host = data[offset:offset + length].decode(errors="ignore")
            offset += length
        else:
            return
        port = struct.unpack("!H", data[offset:offset + 2])[0]
        payload = data[offset + 2:]
        if host == "127.0.0.1":
            host = "::ffff:127.0.0.1"
        remote = (host, port, 0, 0) if ":" in host else (host, port)
        self.inner.datagram_received(payload, remote)

    def error_received(self, exc) -> None:
        callback = getattr(self.inner, "error_received", None)
        if callback:
            callback(exc)

    def connection_lost(self, exc) -> None:
        callback = getattr(self.inner, "connection_lost", None)
        if callback:
            callback(exc)


async def main() -> None:
    xray = os.environ.get("XRAY_BINARY", "").strip()
    asset_dir = os.environ.get("XRAY_LOCATION_ASSET", "").strip()
    if not xray or not asset_dir:
        raise SystemExit("Set XRAY_BINARY and XRAY_LOCATION_ASSET")
    if not Path(xray).is_file() or not os.access(xray, os.X_OK):
        raise SystemExit("XRAY_BINARY must point to an executable file")
    if not Path(asset_dir).is_dir():
        raise SystemExit("XRAY_LOCATION_ASSET must point to a directory")

    proxy_port, target_port = free_port(), free_port()
    cert_dir = tempfile.mkdtemp(prefix="hrcore-quic-cert-")
    cert_path, key_path = Path(cert_dir) / "cert.pem", Path(cert_dir) / "key.pem"
    subprocess.run([
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(key_path), "-out", str(cert_path),
        "-subj", "/CN=allowed.example", "-days", "1",
    ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    server_config = QuicConfiguration(is_client=False, alpn_protocols=["h3"])
    server_config.load_cert_chain(str(cert_path), str(key_path))
    server = await serve("127.0.0.1", target_port, configuration=server_config, create_protocol=ServerProtocol)

    xray_config = {
        "log": {"loglevel": "warning"},
        "inbounds": [{
            "tag": "l7-quic-socks",
            "listen": "127.0.0.1",
            "port": proxy_port,
            "protocol": "socks",
            "settings": {"auth": "noauth", "udp": True},
            "sniffing": {
                "enabled": True,
                "destOverride": ["quic"],
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
                "inboundTag": ["l7-quic-socks"],
                "domain": ["domain:blocked.example"],
                "outboundTag": "block",
            }]
        },
    }
    fd, config_path = tempfile.mkstemp(prefix="hrcore-quic-xray-", suffix=".json", dir="/tmp")
    os.close(fd)
    with open(config_path, "w", encoding="utf-8") as stream:
        json.dump(xray_config, stream)
    process = subprocess.Popen(
        [xray, "run", "-config", config_path],
        env={**os.environ, "XRAY_LOCATION_ASSET": asset_dir},
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )

    try:
        deadline = asyncio.get_running_loop().time() + 8
        while asyncio.get_running_loop().time() < deadline:
            if process.poll() is not None:
                output = process.stdout.read() if process.stdout else ""
                raise RuntimeError(f"Xray exited early: {output}")
            try:
                with socket.create_connection(("127.0.0.1", proxy_port), timeout=0.2):
                    break
            except OSError:
                await asyncio.sleep(0.1)
        else:
            raise RuntimeError("Xray SOCKS inbound did not start")

        loop = asyncio.get_running_loop()
        original_create = loop.create_datagram_endpoint

        async def try_host(server_name: str, timeout: float = 4.0) -> bool:
            global EXPECTED_BLOCKED
            association = SocksUDPAssociation("127.0.0.1", proxy_port)

            async def patched_create_datagram_endpoint(protocol_factory, *args, **kwargs):
                old_sock = kwargs.pop("sock", None)
                if old_sock is not None:
                    old_sock.close()
                udp_sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                udp_sock.bind(("127.0.0.1", 0))
                udp_sock.connect(association.relay)
                inner = protocol_factory()
                adapter = SocksDatagramTransport(association)
                wrapper = SocksDatagramProtocol(inner, adapter)
                base_transport, _ = await original_create(lambda: wrapper, sock=udp_sock)
                adapter.base = base_transport
                return adapter, inner

            loop.create_datagram_endpoint = patched_create_datagram_endpoint
            client_config = QuicConfiguration(is_client=True, alpn_protocols=["h3"])
            client_config.server_name = server_name
            client_config.verify_mode = 0
            connected = False
            EXPECTED_BLOCKED = server_name == "blocked.example"
            try:
                async with asyncio.timeout(timeout):
                    async with connect("127.0.0.1", target_port, configuration=client_config):
                        connected = True
            except (TimeoutError, OSError, ConnectionError):
                pass
            finally:
                await asyncio.sleep(0.1)
                EXPECTED_BLOCKED = False
                loop.create_datagram_endpoint = original_create
                association.close()
            return connected

        blocked = await try_host("blocked.example")
        blocked_handshakes = len(HANDSHAKES)
        allowed = await try_host("allowed.example")
        allowed_handshakes = len(HANDSHAKES) - blocked_handshakes
        if blocked or blocked_handshakes != 0 or not allowed or allowed_handshakes != 1:
            raise AssertionError(
                f"QUIC SNI mismatch: blocked_connected={blocked}, blocked_handshakes={blocked_handshakes}, "
                f"allowed_connected={allowed}, allowed_handshakes={allowed_handshakes}"
            )
        print("L7_QUIC_SNI_ROUTING_E2E_OK: blocked.example -> block, allowed.example -> direct")
    finally:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)
        server.close()
        try:
            os.unlink(config_path)
        except FileNotFoundError:
            pass
        shutil.rmtree(cert_dir, ignore_errors=True)


if __name__ == "__main__":
    loop = asyncio.new_event_loop()
    def exception_handler(loop, context):
        exception = context.get("exception")
        if EXPECTED_BLOCKED and isinstance(exception, ConnectionError):
            return
        loop.default_exception_handler(context)
    loop.set_exception_handler(exception_handler)
    try:
        loop.run_until_complete(main())
    finally:
        loop.close()
