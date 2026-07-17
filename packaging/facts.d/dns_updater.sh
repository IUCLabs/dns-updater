#!/usr/bin/env bash
# Facter external fact: report dns-updater's status by querying its local API
# socket. Emits flat key=value facts (compatible across facter versions).
# Puppet already knows the DESIRED records (it writes the records file); these
# facts report what the daemon actually achieved on the server.
set -u

SOCK="${DNS_UPDATER_API_SOCK:-/run/dns-updater/api.sock}"

emit() { printf 'dns_updater_%s=%s\n' "$1" "$2"; }

if [ ! -S "$SOCK" ]; then
  emit running false
  emit healthy false
  exit 0
fi

json="$(curl -s --max-time 3 --unix-socket "$SOCK" http://local/status 2>/dev/null)"
if [ -z "$json" ]; then
  emit running true
  emit healthy false
  emit last_error "api_unreachable"
  exit 0
fi

emit running true

python3 - "$json" <<'PY'
import json, sys
try:
    d = json.loads(sys.argv[1])
except Exception:
    print("dns_updater_healthy=false")
    print("dns_updater_last_error=bad_json")
    sys.exit(0)

def emit(k, v):
    print(f"dns_updater_{k}={v}")

emit("healthy", str(d.get("healthy", False)).lower())
emit("managed_records", d.get("managed_records", 0))
if d.get("last_reconcile"):
    emit("last_reconcile", d["last_reconcile"])
if d.get("last_change"):
    emit("last_change", d["last_change"])
if d.get("last_error"):
    emit("last_error", d["last_error"].replace("\n", " ")[:200])

zones = d.get("zones") or []
failed = [z for z in zones if (z.get("error") or z.get("rcode", 0) != 0)]
emit("zones_total", len(zones))
emit("zones_failed", len(failed))
if failed:
    emit("failed_zones", ",".join(f"{z['zone']}:{z.get('rcode_text','?')}" for z in failed))
PY
