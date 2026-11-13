#!/usr/bin/env bash
# Isolated transient systemd service test. Never modifies installed units.
set -euo pipefail
: "${HRCORE_TEST_XRAY:?set path to test Xray binary}"
: "${HRCORE_TEST_XRAY_ASSETS:?set directory containing geoip.dat/geosite.dat}"
[[ $HRCORE_TEST_XRAY = /* && $HRCORE_TEST_XRAY_ASSETS = /* ]] || { echo 'absolute paths required' >&2; exit 2; }
root=$(mktemp -d /run/hrcore-systemd-geodata.XXXXXXXX)
unit="hrcore-geodata-test-$$"
cleanup() {
 systemctl stop "$unit.service" >/dev/null 2>&1 || true
 systemctl reset-failed "$unit.service" >/dev/null 2>&1 || true
 rm -rf -- "$root"
}
trap cleanup EXIT
mkdir -m 0700 "$root/legacy" "$root/managed"
cp -- "$HRCORE_TEST_XRAY_ASSETS/geoip.dat" "$HRCORE_TEST_XRAY_ASSETS/geosite.dat" "$root/legacy/"
cp -- "$HRCORE_TEST_XRAY_ASSETS/geoip.dat" "$HRCORE_TEST_XRAY_ASSETS/geosite.dat" "$root/managed/"
cat > "$root/config.json" <<'JSON'
{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","domain":["geosite:google"],"outboundTag":"direct"},{"type":"field","ip":["geoip:private"],"outboundTag":"direct"}]}}
JSON
run_unit() {
 local asset_path="$1"
 systemd-run --quiet --unit="$unit" --property=Type=simple \
  --property=Restart=no --property=NoNewPrivileges=yes \
  --property=ProtectSystem=strict --property=ProtectHome=yes \
  --property=PrivateTmp=yes --property=PrivateDevices=yes \
  --property=RestrictAddressFamilies='AF_UNIX AF_INET AF_INET6 AF_NETLINK' \
  --setenv="XRAY_LOCATION_ASSET=$asset_path" \
  "$HRCORE_TEST_XRAY" run -config "$root/config.json"
 for i in $(seq 1 30); do
  if [[ $(systemctl is-active "$unit.service" || true) = active ]]; then
   sleep 0.3
   [[ $(systemctl is-active "$unit.service" || true) = active ]] && return 0
  fi
  sleep 0.1
 done
 journalctl -u "$unit.service" -n 15 --no-pager >&2 || true
 return 1
}
stop_unit() {
 systemctl stop "$unit.service" >/dev/null 2>&1 || true
 [[ $(systemctl is-active "$unit.service" || true) != active ]]
 systemctl reset-failed "$unit.service" >/dev/null 2>&1 || true
}
run_unit "$root/legacy"
stop_unit
run_unit "$root/managed"
stop_unit
# Invalid managed assets must fail without changing the original assets.
mkdir -m 0700 "$root/broken"
printf 'invalid database' > "$root/broken/geoip.dat"
cp -- "$root/legacy/geosite.dat" "$root/broken/geosite.dat"
stop_unit
if run_unit "$root/broken" >/dev/null 2>&1; then
 echo 'FAIL: systemd accepted broken GeoData assets' >&2
 exit 1
fi
stop_unit
run_unit "$root/legacy"
for name in geoip.dat geosite.dat; do cmp -- "$root/legacy/$name" "$HRCORE_TEST_XRAY_ASSETS/$name"; done
printf 'PASS: transient systemd service legacy -> managed -> legacy; original assets unchanged; failed managed start recovered to legacy; final service active\n'
