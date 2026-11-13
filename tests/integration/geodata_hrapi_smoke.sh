#!/usr/bin/env bash
# Opt-in full hrapi + Xray smoke test; no installed services are touched.
set -euo pipefail
: "${HRCORE_TEST_XRAY:?set absolute path to Xray}"
: "${HRCORE_TEST_XRAY_ASSETS:?set absolute path to GeoData assets}"
: "${HRCORE_TEST_GO:?set absolute path to Go executable}"
root=$(mktemp -d /var/tmp/hrcore-hrapi-geodata.XXXXXXXX)
pid=''
cleanup() {
 if [[ -n $pid ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
 rm -rf -- "$root"
}
trap cleanup EXIT
mkdir -m 0700 "$root/state" "$root/config" "$root/legacy" "$root/state/geodata"
cp -- "$HRCORE_TEST_XRAY_ASSETS/geoip.dat" "$HRCORE_TEST_XRAY_ASSETS/geosite.dat" "$root/legacy/"
cat > "$root/config/xray.json" <<'JSON'
{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","domain":["geosite:google"],"outboundTag":"direct"},{"type":"field","ip":["geoip:private"],"outboundTag":"direct"}]}}
JSON
chmod 0600 "$root/config/xray.json"
"$HRCORE_TEST_GO" build -o "$root/hrapi" ./cmd/hrapi
# The baseline must be a validated generation; the legacy directory is not altered.
mkdir -m 0700 "$root/state/geodata/generation-0"
cp -- "$root/legacy/geoip.dat" "$root/legacy/geosite.dat" "$root/state/geodata/generation-0/"
ln -s generation-0 "$root/state/geodata/current"
port=$(python3 - <<'PY'
import socket
s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1]);s.close()
PY
)
start_app() {
 local assets="$1"
 XRAY_LOCATION_ASSET="$assets" "$root/hrapi" -listen "127.0.0.1:$port" -xray "$HRCORE_TEST_XRAY" -config "$root/config/xray.json" -state-dir "$root/state" -restore-config=true -autostart >"$root/hrapi.log" 2>&1 &
 pid=$!
 for i in $(seq 1 60); do
  if ! kill -0 "$pid" 2>/dev/null; then cat "$root/hrapi.log" >&2; return 1; fi
  if curl -fsS --max-time 1 "http://127.0.0.1:$port/v1/runtime" > "$root/runtime.json" 2>/dev/null; then
   if python3 - "$root/runtime.json" <<'PY'
import json,sys
v=json.load(open(sys.argv[1]));sys.exit(0 if v.get('running') and v.get('configured') else 1)
PY
   then return 0; fi
  fi
  sleep 0.2
 done
 cat "$root/hrapi.log" >&2
 return 1
}
stop_app() { kill "$pid"; wait "$pid" 2>/dev/null || true; pid=''; }
start_app "$root/legacy"
stop_app
start_app "$root/state/geodata/current"
curl -fsS --max-time 3 "http://127.0.0.1:$port/v1/geodata/status" > "$root/status.json"
python3 - "$root/status.json" <<'PY'
import json,sys
v=json.load(open(sys.argv[1]));assert v.get('update',{}).get('ready'),v
PY
stop_app
start_app "$root/legacy"
for name in geoip.dat geosite.dat; do cmp -- "$root/legacy/$name" "$HRCORE_TEST_XRAY_ASSETS/$name"; done
printf 'PASS: full hrapi + Xray autostart legacy -> managed -> legacy, runtime healthy, assets intact\n'
