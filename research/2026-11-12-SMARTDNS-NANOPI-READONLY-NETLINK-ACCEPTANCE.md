# SmartDNS 48.4 — NanoPi ARM64 isolated runtime acceptance

Date: 2026-11-12

## Artifact

- Staged executable: `/home/pi/chatgpt/hydraroute-arm64-debian/ubuntu/staging-smartdns-48.4-aarch64`
- Version: SmartDNS 48.4 (Release48.4), AArch64
- SHA-256: `79bc9b5037f6ac68be7ee8bcd4a5040421ebbfefae17e4b629de7a20a80b22c8`
- This remains a project staging artifact, not an installed system binary.

## Isolated functional test

Ran a temporary adaptation of the PR #7 profile-pinned SOCKS/DoH integration harness as user `pi`. The Core Go policy-front invocation was omitted because the NanoPi does not contain the PR source tree; that policy-front E2E remains a separate repository Go integration test.

- SmartDNS bound only loopback DNS UDP/TCP on 127.0.0.1:26053.
- Two local fake DoH endpoints used 127.0.0.1:29443 and :29444, reached only through independent SOCKS5 endpoints 127.0.0.1:26081 and :26082.
- Static address rule and block/allow domain-set behavior passed.
- TLS certificate validation and both expected SNI names passed.
- Foreign and Torrent DoH traffic stayed on their own SOCKS5 profiles.
- Disabling the Foreign SOCKS endpoint made Foreign fail closed while Torrent continued to resolve.
- Unmatched domains failed closed.
- The complete functional harness passed.

## NETLINK trace interpretation

The first strict trace pass found SmartDNS opening two `NETLINK_ROUTE` sockets. The complete trace showed only:

- one subscription with `nl_groups=0x000004`, which Linux headers identify as `RTMGRP_NEIGH`;
- one `RTM_GETNEIGH` (message type 0x1e) request with `NLM_F_REQUEST|0x300` (dump flags).

This is a read-only neighbor-table dump/subscription, not a route or neighbor-table mutation. The trace contained no other netlink requests. Additional checks passed: no read of `/etc/resolv.conf`, no execution of `ip`, `iptables`, `nft`, `ipset` or `conntrack`, and no non-loopback bind or IP network I/O.

The CI harness was corrected to allow only this exact read-only NETLINK_ROUTE behavior and to continue rejecting other netlink families, groups or requests. This avoids a false-positive while retaining the no-host-network-mutation guard.

## Safety and boundary

- The test used only temporary files/processes and loopback sockets; alternate high ports were selected because TCP port 18443 was already occupied by an unrelated listener.
- The SmartDNS process was stopped by the harness; all test ports were released.
- No systemd service was installed or started; no persistent SmartDNS configuration was written.
- No Core DNS policy-front runtime was deployed. No system resolver, route, firewall, interface, Keenetic, HRNeo, 3x-ui or Passwall2 state was changed.
