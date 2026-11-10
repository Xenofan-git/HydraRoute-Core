# SmartDNS dependency and patch policy

- Upstream project: SmartDNS, GPL-3.0-or-later.
- Pinned release: `Release48.4`.
- Pinned commit: `21c940edc65520849ba03544c5cf8d9cf326e680`.
- The Core does not vendor the upstream source tree or its binary. CI/build tooling must fetch this exact commit and apply the patches in `patches/` before compiling.
- `patches/0001-disable-netlink-without-mac.patch` disables neighbor-table NETLINK_ROUTE setup when MAC-based client rules are absent. Core config must also set `local-ptr-enable no` to prevent local-address NETLINK_ROUTE monitoring.
- The SmartDNS config compiler is `internal/dnsservice/smartdns`; it emits only typed directives and does not accept raw user-supplied config fragments.
- The previously built ARM64 artifact in `/root/hrdns-research/d0/artifacts/` is an unpatched baseline and is not a release artifact.
- Do not install or run the sidecar on NanoPi until patched ARM64 build, sandbox runtime, no-netlink, TLS verification, per-profile SOCKS isolation and rollback gates pass, and deployment is explicitly approved.
