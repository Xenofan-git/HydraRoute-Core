# HydraRoute Core deployment template

This directory contains deployment templates only. Nothing here has been installed or enabled on NanoPi or the VPS.

- `systemd/hydraroute-core.service`: hardened systemd unit. It runs as an unprivileged `hydraroute` user, binds the API to loopback, validates/restores config at startup, uses explicit autostart, and schedules refresh every six hours with capped retries.
- `sysusers/hydraroute-core.conf`: system user declaration.

The unit has no host network capabilities and does not modify routes, firewall, DNS or interfaces. It needs ordinary outbound network access for subscription fetch and Xray proxy connections. Administer the loopback API only through an authenticated SSH/Tailscale tunnel.

Before installing, review `ExecStart`. It includes `-autostart`: Xray starts after a validated saved config is restored, and after successful scheduled/manual refresh. On a first install with no saved config the service remains up; add one or more profiles and call `POST /v1/profiles/refresh` to prepare the first config. If automatic Xray start is not desired, remove `-autostart`.

Profile encryption key path: `/var/lib/hydraroute/state/profiles/profiles.key`. Back it up together with encrypted profile files.

This repository change does not install or enable the unit. Enable it only during an approved deployment.


## Automatic recovery after reboot and process failure

Enable the Core unit at installation time so systemd starts it on every boot:

```sh
sudo systemctl enable --now hydraroute-core.service
```

The unit uses `Restart=on-failure` with a five-second delay and no start-rate lockout. Its `-autostart` option restores the last validated Xray configuration and starts Xray. When SmartDNS is enabled in the saved configuration, Core starts its sidecar after the Core DNS policy resolver is ready. Core supervises unexpected Xray and SmartDNS exits with bounded exponential retry delays (5 seconds up to 5 minutes); an explicit Stop action disables recovery for that component until it is started again. Health Monitor probes SmartDNS DNS readiness as well as process state.

A first-install system with no saved Xray configuration remains safely unconfigured; add profiles and prepare a valid configuration before expecting Xray to start. SmartDNS remains off unless its saved configuration is enabled.
