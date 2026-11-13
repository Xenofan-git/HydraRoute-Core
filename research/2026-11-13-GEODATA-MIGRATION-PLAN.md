# GeoData migration — development-only design

Current deployment uses `/usr/local/libexec/hydraroute` as both `WorkingDirectory` and `XRAY_LOCATION_ASSET`. The service's `ProtectSystem=strict` and `ReadWritePaths=/var/lib/hydraroute` prohibit modifying the existing asset directory. Do not overwrite it.

Target layout (not yet deployed):

- `/var/lib/hydraroute/geodata/generation-0/{geoip.dat,geosite.dat}` — imported baseline, immutable once published
- `/var/lib/hydraroute/geodata/generation-<timestamp>/{geoip.dat,geosite.dat}` — future generations
- `/var/lib/hydraroute/geodata/current` — atomic symlink pointing to one generation
- `XRAY_LOCATION_ASSET=/var/lib/hydraroute/geodata/current` — only after successful migration and integration tests

`Bootstrap` now safely copies the old pair into the private store and runs an Xray preflight. It does NOT change the service unit or running process. Before deployment: validate actual Xray binary and configuration with both the old and new asset directories; ensure Xray resolves its other assets (including additional `ext:` databases and configuration dependencies) from the new directory; test Watchlist asset path and symlink handling; ensure restart and post-restart health checks; persist an update transaction journal and recover interrupted updates on boot; coordinate generation switches with the supervisor's operation lock; verify file permissions and ownership for the `hydraroute` user; test failure injection and power-loss recovery. Avoid switching the service to a symlink path until all these tests pass.

Rollback must revert the generation pointer AND restart/revalidate Xray, not merely change the symlink. Preserve the original systemd unit and old assets as an independent rollback route.
