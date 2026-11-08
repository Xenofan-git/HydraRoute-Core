# HydraRoute Core deployment template

This directory contains deployment templates only. Nothing here has been installed or enabled on NanoPi or the VPS.

- `systemd/hydraroute-core.service`: hardened systemd unit. It runs as an unprivileged `hydraroute` user, binds the API to loopback, validates/restores config at startup, uses explicit autostart, and schedules refresh every six hours with capped retries.
- `sysusers/hydraroute-core.conf`: system user declaration.

The unit has no host network capabilities and does not modify routes, firewall, DNS or interfaces. It needs ordinary outbound network access for subscription fetch and Xray proxy connections. Administer the loopback API only through an authenticated SSH/Tailscale tunnel.

Before installing, review `ExecStart`. It includes `-autostart`: Xray starts after a validated saved config is restored, and after successful scheduled/manual refresh. On a first install with no saved config the service remains up; add one or more profiles and call `POST /v1/profiles/refresh` to prepare the first config. If automatic Xray start is not desired, remove `-autostart`.

Profile encryption key path: `/var/lib/hydraroute/state/profiles/profiles.key`. Back it up together with encrypted profile files.

Do not install or enable this unit without explicit operator approval.
