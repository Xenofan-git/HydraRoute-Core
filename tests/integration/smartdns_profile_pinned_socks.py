#!/usr/bin/env python3
"""Exercise SmartDNS TLS/DoH upstreams through profile-pinned SOCKS5 proxies."""
from __future__ import annotations

import os
import re
import signal
import shutil
import socket
import socketserver
import ssl
import struct
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

SMARTDNS = os.environ.get("SMARTDNS_BINARY", "")
DNS_TIMEOUT = float(os.environ.get("SMARTDNS_DNS_TIMEOUT", "3"))
STARTUP_TIMEOUT = float(os.environ.get("SMARTDNS_STARTUP_TIMEOUT", "10"))
if not SMARTDNS or not Path(SMARTDNS).is_file():
    raise SystemExit("SMARTDNS_BINARY must point to the built SmartDNS executable")

REPO_ROOT = Path(__file__).resolve().parents[2]
ROOT = Path(tempfile.mkdtemp(prefix="hrcore-smartdns-sandbox-"))
CERTS = ROOT / "certs"
DATA = ROOT / "data"
CERTS.mkdir(); DATA.mkdir()
HITS: list[tuple[int, str]] = []
PROXY_EVENTS: list[tuple[int, str, int]] = []
SNI: list[str | None] = []
DISABLED_PROXIES: set[int] = set()
ACTIVE_PROXY_CLIENTS: dict[int, set[socket.socket]] = {}
LOCK = threading.Lock()
HTTP_SERVERS: list[ThreadingHTTPServer] = []
SOCKS_SERVERS: list[socketserver.ThreadingTCPServer] = []
SMARTDNS_PROCESS: subprocess.Popen | None = None
LOG_FILE = None


def run_checked(args: list[str]) -> None:
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def create_test_ca() -> None:
    run_checked([
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(CERTS / "ca.key"), "-out", str(CERTS / "ca.crt"),
        "-days", "2", "-subj", "/CN=HydraRoute SmartDNS Test CA",
        "-addext", "basicConstraints=critical,CA:TRUE",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign",
    ])
    run_checked([
        "openssl", "req", "-newkey", "rsa:2048", "-nodes",
        "-keyout", str(CERTS / "server.key"), "-out", str(CERTS / "server.csr"),
        "-subj", "/CN=dns-foreign.example.test",
    ])
    (CERTS / "server.ext").write_text(
        "subjectAltName=DNS:dns-foreign.example.test,DNS:dns-torrent.example.test\n"
        "basicConstraints=CA:FALSE\n"
        "keyUsage=digitalSignature,keyEncipherment\n"
        "extendedKeyUsage=serverAuth\n"
    )
    run_checked([
        "openssl", "x509", "-req", "-in", str(CERTS / "server.csr"),
        "-CA", str(CERTS / "ca.crt"), "-CAkey", str(CERTS / "ca.key"),
        "-CAcreateserial", "-out", str(CERTS / "server.crt"), "-days", "2",
        "-extfile", str(CERTS / "server.ext"),
    ])
    run_checked(["openssl", "rehash", str(CERTS)])
    (DATA / "ads.txt").write_text("ads.example.test\nblocked.example.test\nblocked.allow-parent.example.test\n")
    (DATA / "allow.txt").write_text("safe.ads.example.test\nexact.example.test\nallow-parent.example.test\n")


def read_exact(sock: socket.socket, size: int) -> bytes:
    data = bytearray()
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise EOFError("short SOCKS5 read")
        data.extend(chunk)
    return bytes(data)


def parse_question(packet: bytes) -> tuple[str, bytes, bytes, bytes]:
    if len(packet) < 17:
        raise ValueError("short DNS message")
    offset = 12
    labels: list[str] = []
    while True:
        length = packet[offset]
        offset += 1
        if length == 0:
            break
        labels.append(packet[offset:offset + length].decode("ascii"))
        offset += length
    qname = ".".join(labels).lower()
    qtype = packet[offset:offset + 2]
    qclass = packet[offset + 2:offset + 4]
    offset += 4
    return qname, qtype, qclass, packet[12:offset]


def make_dns_response(packet: bytes) -> bytes:
    _, qtype, qclass, question = parse_question(packet)
    flags = 0x8180  # QR, RD, RA, NOERROR
    if qtype != b"\x00\x01":
        return packet[:2] + struct.pack("!HHHHH", flags, 1, 0, 0, 0) + question
    answer = b"\xc0\x0c" + qtype + qclass + struct.pack("!IH", 30, 4) + socket.inet_aton("203.0.113.77")
    return packet[:2] + struct.pack("!HHHHH", flags, 1, 1, 0, 0) + question + answer


class DoHHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        qname, _, _, _ = parse_question(body)
        with LOCK:
            HITS.append((self.server.server_port, qname))
        response = make_dns_response(body)
        self.send_response(200)
        self.send_header("Content-Type", "application/dns-message")
        self.send_header("Content-Length", str(len(response)))
        self.end_headers()
        self.wfile.write(response)

    def log_message(self, *_args) -> None:
        pass


def start_doh_servers() -> None:
    for port in (18443, 18444):
        server = ThreadingHTTPServer(("127.0.0.1", port), DoHHandler)
        server.daemon_threads = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(str(CERTS / "server.crt"), str(CERTS / "server.key"))

        def record_sni(_sock, server_name, _context):
            with LOCK:
                SNI.append(server_name)

        context.set_servername_callback(record_sni)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        HTTP_SERVERS.append(server)


class SocksHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        client = self.request
        proxy_port = self.server.server_address[1]
        with LOCK:
            ACTIVE_PROXY_CLIENTS.setdefault(proxy_port, set()).add(client)
        client.settimeout(6)
        try:
            version, method_count = read_exact(client, 2)
            if version != 5:
                return
            read_exact(client, method_count)
            client.sendall(b"\x05\x00")
            version, command, _reserved, address_type = read_exact(client, 4)
            if version != 5 or command != 1:
                return
            if address_type == 1:
                host = socket.inet_ntoa(read_exact(client, 4))
            elif address_type == 3:
                host_length = read_exact(client, 1)[0]
                host = read_exact(client, host_length).decode("ascii")
            elif address_type == 4:
                host = socket.inet_ntop(socket.AF_INET6, read_exact(client, 16))
            else:
                return
            target_port = struct.unpack("!H", read_exact(client, 2))[0]
            with LOCK:
                PROXY_EVENTS.append((proxy_port, host, target_port))
            if proxy_port in DISABLED_PROXIES:
                client.sendall(b"\x05\x05\x00\x01\x7f\x00\x00\x01\x00\x00")
                return
            upstream = socket.create_connection((host, target_port), timeout=5)
            client.sendall(b"\x05\x00\x00\x01\x7f\x00\x00\x01\x00\x00")
            client.settimeout(None)
            upstream.settimeout(None)
            finished = threading.Event()

            def relay(source: socket.socket, destination: socket.socket) -> None:
                try:
                    while True:
                        chunk = source.recv(65536)
                        if not chunk:
                            break
                        destination.sendall(chunk)
                except OSError:
                    pass
                finally:
                    try:
                        destination.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass
                    finished.set()

            threading.Thread(target=relay, args=(client, upstream), daemon=True).start()
            relay(upstream, client)
            finished.wait(1)
            upstream.close()
        except (OSError, EOFError, ValueError):
            try:
                client.sendall(b"\x05\x05\x00\x01\x7f\x00\x00\x01\x00\x00")
            except OSError:
                pass
        finally:
            with LOCK:
                ACTIVE_PROXY_CLIENTS.get(proxy_port, set()).discard(client)


def disable_proxy(port: int) -> None:
    with LOCK:
        DISABLED_PROXIES.add(port)
        clients = list(ACTIVE_PROXY_CLIENTS.get(port, set()))
    for client in clients:
        try:
            client.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            client.close()
        except OSError:
            pass


class SocksServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def start_socks_servers() -> None:
    for port in (16081, 16082):
        server = SocksServer(("127.0.0.1", port), SocksHandler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        SOCKS_SERVERS.append(server)


def build_config() -> Path:
    debug_log = ROOT / "smartdns-debug.log"
    config = f"""bind 127.0.0.1:16053
bind-tcp 127.0.0.1:16053
cache-size 4096
cache-persist no
resolv-file /dev/null
local-ptr-enable no
mdns-lookup no
expand-ptr-from-address no
audit-enable no
log-level debug
log-file {debug_log}
speed-check-mode none
ca-path {CERTS}
proxy-server socks5://127.0.0.1:16081 -name hr_proxy_foreign
proxy-server socks5://127.0.0.1:16082 -name hr_proxy_torrent
server-https https://dns-foreign.example.test:18443/dns-query -group hr_dns_foreign -exclude-default-group -proxy hr_proxy_foreign -host-name dns-foreign.example.test -tls-host-verify dns-foreign.example.test -host-ip 127.0.0.1 -http-host dns-foreign.example.test
server-https https://dns-torrent.example.test:18444/dns-query -group hr_dns_torrent -exclude-default-group -proxy hr_proxy_torrent -host-name dns-torrent.example.test -tls-host-verify dns-torrent.example.test -host-ip 127.0.0.1 -http-host dns-torrent.example.test
domain-set -name hr_set_ads -type list -file {DATA / 'ads.txt'}
domain-set -name hr_set_allow -type list -file {DATA / 'allow.txt'}
address /domain-set:hr_set_ads/#
address /domain-set:hr_set_allow/-
address /allowed.example.test/203.0.113.5
nameserver /foreign.test/hr_dns_foreign
nameserver /torrent.test/hr_dns_torrent
nameserver /example.test/hr_dns_foreign
"""
    path = ROOT / "smartdns.conf"
    path.write_text(config)
    return path


def dns_query(name: str, timeout: float | None = None) -> str | None:
    if timeout is None:
        timeout = DNS_TIMEOUT
    ident = int(time.time_ns()) & 0xFFFF
    qname = b"".join(bytes([len(label)]) + label.encode("ascii") for label in name.split(".")) + b"\x00"
    packet = struct.pack("!HHHHHH", ident, 0x0100, 1, 0, 0, 1) + qname + struct.pack("!HH", 1, 1)
    packet += b"\x00\x00\x29\x04\xd0\x00\x00\x00\x00\x00\x00"
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        sock.sendto(packet, ("127.0.0.1", 16053))
        response, _ = sock.recvfrom(4096)
        if len(response) < 12:
            return None
        answer_count = struct.unpack("!H", response[6:8])[0]
        if answer_count == 0:
            return None
        offset = 12
        while response[offset] != 0:
            offset += response[offset] + 1
        offset += 5  # zero label + QTYPE + QCLASS
        for _ in range(answer_count):
            if response[offset] & 0xC0 == 0xC0:
                offset += 2
            else:
                while response[offset] != 0:
                    offset += response[offset] + 1
                offset += 1
            record_type = struct.unpack("!H", response[offset:offset + 2])[0]
            data_length = struct.unpack("!H", response[offset + 8:offset + 10])[0]
            offset += 10
            data = response[offset:offset + data_length]
            offset += data_length
            if record_type == 1 and data_length == 4:
                return socket.inet_ntoa(data)
        return None
    except (OSError, IndexError, struct.error):
        return None
    finally:
        sock.close()


def start_smartdns(config_path: Path) -> tuple[subprocess.Popen, Path | None, object]:
    log_path = ROOT / "smartdns.log"
    log_file = open(log_path, "w", encoding="utf-8")
    strace_path = ROOT / "smartdns.strace"
    use_strace = bool(shutil.which("strace")) and os.environ.get("SMARTDNS_NO_STRACE") != "1"
    if use_strace:
        command = [
            "strace", "-f", "-qq", "-e", "trace=socket,bind,connect,sendto,sendmsg,openat,execve",
            "-o", str(strace_path), SMARTDNS, "-f", "-c", str(config_path), "-p", "-",
        ]
    else:
        command = [SMARTDNS, "-f", "-c", str(config_path), "-p", "-"]
    process = subprocess.Popen(command, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True)
    return process, (strace_path if use_strace else None), log_file


def verify_strace(trace_path: Path | None) -> None:
    if trace_path is None:
        print("SKIP: strace unavailable; runtime proxy/TLS checks still executed")
        return
    trace = trace_path.read_text(errors="replace")
    if re.search(r"socket\((AF|PF)_NETLINK", trace):
        raise AssertionError("SmartDNS opened a NETLINK socket")
    if '"/etc/resolv.conf"' in trace:
        raise AssertionError("SmartDNS read the host /etc/resolv.conf")
    if re.search(r'execve\(".*/(ip|iptables|ip6tables|nft|ipset|conntrack)"', trace):
        raise AssertionError("SmartDNS attempted to execute a host networking tool")
    dns_binds = [line for line in trace.splitlines() if "bind(" in line and "htons(16053)" in line]
    if not dns_binds or any('inet_addr("127.0.0.1")' not in line for line in dns_binds):
        raise AssertionError(f"SmartDNS DNS listener is not loopback-only: {dns_binds}")
    network_binds = [line for line in trace.splitlines() if "bind(" in line and ("AF_INET" in line or "AF_INET6" in line)]
    for line in network_binds:
        if 'inet_addr("127.0.0.1")' not in line and 'inet_pton(AF_INET6, "::1")' not in line and 'inet6_addr("::1")' not in line:
            raise AssertionError(f"SmartDNS attempted a non-loopback bind: {line}")
    network_ops = [line for line in trace.splitlines() if any(call in line for call in ("connect(", "sendto(", "sendmsg(", "sendmmsg(")) and ("AF_INET" in line or "AF_INET6" in line)]
    for line in network_ops:
        if 'inet_addr("127.0.0.1")' not in line and 'inet_pton(AF_INET6, "::1")' not in line and 'inet6_addr("::1")' not in line:
            raise AssertionError(f"SmartDNS attempted non-loopback network I/O: {line}")


def cleanup() -> None:
    global SMARTDNS_PROCESS, LOG_FILE
    if SMARTDNS_PROCESS is not None and SMARTDNS_PROCESS.poll() is None:
        try:
            os.killpg(SMARTDNS_PROCESS.pid, signal.SIGTERM)
            SMARTDNS_PROCESS.wait(timeout=5)
        except (ProcessLookupError, subprocess.TimeoutExpired):
            try:
                os.killpg(SMARTDNS_PROCESS.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            SMARTDNS_PROCESS.wait(timeout=3)
    if LOG_FILE is not None:
        LOG_FILE.close()
    for server in SOCKS_SERVERS:
        server.shutdown(); server.server_close()
    for server in HTTP_SERVERS:
        server.shutdown(); server.server_close()


def main() -> None:
    global SMARTDNS_PROCESS, LOG_FILE
    create_test_ca()
    start_doh_servers()
    start_socks_servers()
    config_path = build_config()
    SMARTDNS_PROCESS, trace_path, LOG_FILE = start_smartdns(config_path)
    try:
        deadline = time.time() + STARTUP_TIMEOUT
        while time.time() < deadline:
            if dns_query("allowed.example.test", 1) == "203.0.113.5":
                break
            if SMARTDNS_PROCESS.poll() is not None:
                log_text = (ROOT / "smartdns.log").read_text(errors="replace")
                trace_text = Path(trace_path).read_text(errors="replace")[-8000:] if trace_path else "(strace disabled)"
                raise AssertionError(f"SmartDNS exited early rc={SMARTDNS_PROCESS.returncode}; log: {log_text}; trace: {trace_text}")
            time.sleep(0.2)
        assert dns_query("allowed.example.test") == "203.0.113.5", "static allow response failed"
        before_ads = len(HITS)
        ads_result = dns_query("ads.example.test")
        blocked_result = dns_query("blocked.example.test")
        assert ads_result != "203.0.113.77", f"block domain-set query escaped to DoH: {ads_result}"
        assert blocked_result != "203.0.113.77", f"second block domain-set query escaped to DoH: {blocked_result}"
        with LOCK:
            assert len(HITS) == before_ads, "blocked domain-set query unexpectedly reached a DoH upstream"
        # SmartDNS DOMAIN_FLAG_ADDR_IGN must override a matching parent block
        # suffix. Exact Block/Allow duplicates are normalized out of the block
        # set by the Core compiler before this configuration reaches SmartDNS.
        assert dns_query("safe.ads.example.test") == "203.0.113.77", "child allow exception did not override parent block"
        assert dns_query("exact.example.test") == "203.0.113.77", "normalized exact allow exception did not reach upstream"
        # Engine-behavior regression probe, not an HRDNS-parity expectation:
        # SmartDNS 48.4 accumulates the parent allow flag and therefore lets this
        # query through. HRDNS instead evaluates the most-specific matching
        # suffix first and blocks this child. The Core compiler must reject
        # this unrepresentable overlap until a Core-owned policy resolver is
        # wired in; this raw SmartDNS fixture records the engine limitation.
        assert dns_query("blocked.allow-parent.example.test") == "203.0.113.77", "SmartDNS native parent-allow behavior changed"
        with LOCK:
            assert any(name == "safe.ads.example.test" for _port, name in HITS), HITS
            assert any(name == "exact.example.test" for _port, name in HITS), HITS
            assert any(name == "blocked.allow-parent.example.test" for _port, name in HITS), HITS
        assert dns_query("login.foreign.test") == "203.0.113.77", "DoH over foreign profile SOCKS failed"
        assert dns_query("tracker.torrent.test") == "203.0.113.77", "DoH over torrent profile SOCKS failed"
        with LOCK:
            assert any(port == 18443 and name == "login.foreign.test" for port, name in HITS), HITS
            assert any(port == 18444 and name == "tracker.torrent.test" for port, name in HITS), HITS
            assert any(proxy == 16081 and target_port == 18443 for proxy, _host, target_port in PROXY_EVENTS), PROXY_EVENTS
            assert any(proxy == 16082 and target_port == 18444 for proxy, _host, target_port in PROXY_EVENTS), PROXY_EVENTS
            assert "dns-foreign.example.test" in SNI and "dns-torrent.example.test" in SNI, SNI
            before_policy_front = len(HITS)

        # Exercise the actual Core DNS policy resolver in front of this live
        # pinned SmartDNS process. This proves the HRDNS parent-allow/child-block
        # case is stopped before SmartDNS, while allowed queries still use the
        # profile-pinned SOCKS/DoH groups.
        policy_env = os.environ.copy()
        policy_env["HRCORE_SMARTDNS_TEST_ADDR"] = "127.0.0.1:16053"
        subprocess.run(
            [
                "go", "test", "./internal/dnsservice",
                "-run", "^TestPolicyFrontAgainstRunningSmartDNS$",
                "-count=1",
            ],
            cwd=REPO_ROOT,
            env=policy_env,
            check=True,
        )
        with LOCK:
            policy_hits = HITS[before_policy_front:]
            assert not any(name == "blocked.allow-parent.example.test" for _port, name in policy_hits), policy_hits
            assert any(name == "policy.safe.ads.example.test" for _port, name in policy_hits), policy_hits
            assert any(port == 18443 and name == "policy-login.foreign.test" for port, name in policy_hits), policy_hits
            assert any(port == 18444 and name == "policy-tracker.torrent.test" for port, name in policy_hits), policy_hits
            before_foreign_failure = len(HITS)
        disable_proxy(16081)
        assert dns_query("login2.foreign.test") != "203.0.113.77", "foreign DNS bypassed its failed profile SOCKS proxy"
        with LOCK:
            assert len(HITS) == before_foreign_failure, f"foreign query reached DoH after its proxy was disabled: {HITS[before_foreign_failure:]}"
        assert dns_query("tracker2.torrent.test") == "203.0.113.77", "torrent profile was affected by foreign proxy failure"
        with LOCK:
            assert any(port == 18444 and name == "tracker2.torrent.test" for port, name in HITS), HITS
            before_unmatched = len(HITS)
        assert dns_query("unmatched.test") != "203.0.113.77", "unmatched domain unexpectedly resolved"
        with LOCK:
            assert len(HITS) == before_unmatched, "unmatched domain unexpectedly reached a DoH upstream"
        print("PASS: static/domain-set rules, TLS/SNI verification, profile-pinned SOCKS, per-profile failure isolation, fail-closed default group")
        print("DoH hits:", HITS)
        print("SOCKS routes:", PROXY_EVENTS)
        print("TLS SNI:", SNI)
    except AssertionError:
        debug_path = ROOT / "smartdns-debug.log"
        if LOG_FILE is not None:
            LOG_FILE.write("\\n=== SmartDNS debug log (tail) ===\\n")
            if debug_path.is_file():
                LOG_FILE.write(debug_path.read_text(errors="replace")[-24000:])
            with LOCK:
                LOG_FILE.write(f"\\n=== HITS ===\\n{HITS}\\n=== SOCKS EVENTS ===\\n{PROXY_EVENTS}\\n=== SNI ===\\n{SNI}\\n")
            LOG_FILE.flush()
        raise
    finally:
        cleanup()
    verify_strace(Path(trace_path) if trace_path else None)
    print("SmartDNS isolation trace verified; logs:", ROOT)


main()