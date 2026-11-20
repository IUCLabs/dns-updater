# dns-updater

A small Go daemon that keeps a host's DNS records current on a BIND server via
TSIG-signed **RFC2136 dynamic updates**. It replaces the puppet
`profiles::dns::updater` shell script + `dns-update.path`/`dns-update.service`
systemd pair with a single long-running service.

## What it does

- Reads a **desired-records file** (`zone|name|type|ttl|value`, the same format
  `profiles::dns::record` already emits).
- Pushes the **delta** to the server — one TSIG-signed UPDATE message per zone,
  so one bad zone (e.g. a `NOTZONE`) cannot abort the others.
- Re-reconciles when:
  - the records file changes (inotify on the directory, so atomic replace is
    caught),
  - a **new interface address** appears (DHCP assign/renew) — address
    *removals* (interface down) are ignored, so a transient link drop never
    disturbs records; loopback/link-local are ignored too,
  - a periodic safety-net timer fires,
  - it receives `SIGHUP`.
- Exposes a local **status API** (unix socket) for facter and health checks.

Names are qualified correctly: a record name already ending in `.` is used
verbatim, so there is no `..` empty-label bug — and a malformed record is
*rejected with a clear error* instead of being sent as broken wire data.

## Why native RFC2136 (not `nsupdate`)

The daemon talks the update protocol directly (`github.com/miekg/dns`), so every
zone update carries a structured server **RCODE** and error. That is the
observability the shell version lacked — a bad name or a missing zone shows up
immediately in the logs and the status API instead of an opaque
`nsupdate ... failed`.

## Configuration

Flags or env (see `packaging/env.sample`); env wins via the systemd
`EnvironmentFile`:

| flag | env | default |
|------|-----|---------|
| `-server` | `DNS_UPDATER_SERVER` | (required) |
| `-key-file` | `DNS_UPDATER_KEY_FILE` | `/etc/dns-updater/key` |
| `-records-file` | `DNS_UPDATER_RECORDS_FILE` | `/var/lib/dns-updater/records` |
| `-state-file` | `DNS_UPDATER_STATE_FILE` | `/var/lib/dns-updater/applied` |
| `-watch-interfaces` | `DNS_UPDATER_WATCH_INTERFACES` | `true` |
| `-resync` | `DNS_UPDATER_RESYNC` | `10m` |
| `-api` | `DNS_UPDATER_API` | `/run/dns-updater/api.sock` |
| `-log-level` | `DNS_UPDATER_LOG_LEVEL` | `info` |
| `-oneshot` | `DNS_UPDATER_ONESHOT` | `false` |
| `-deny-ranges` | `DNS_UPDATER_DENY_RANGES` | (none) |
| `-allow-ranges` | `DNS_UPDATER_ALLOW_RANGES` | (none) |
| `-deny-domains` | `DNS_UPDATER_DENY_DOMAINS` | (none) |
| `-allow-domains` | `DNS_UPDATER_ALLOW_DOMAINS` | (none) |

The TSIG key file is BIND format (`key "name" { algorithm ...; secret "..."; };`).

### Filtering

`*-ranges` are comma-separated CIDRs; `*-domains` are comma-separated FQDN
suffixes. Range rules apply to records that carry an address — A/AAAA by value,
PTR by the address encoded in the reverse-DNS owner — so both
`node01-kube-lb0 A 203.0.113.2` and the matching `…113.0.203.in-addr.arpa`
PTR are dropped by `-deny-ranges=203.0.113.0/25`. Domain rules apply to every
record by owner name. Deny wins; a non-empty allow list means "only these".
This keeps k8s/LB/internal addresses (pod/service CIDRs, LB VIP ranges) out of
the authoritative zones and stops NOTAUTH updates for zones the server does not
host.

## Status API

- `GET /status` → JSON: health, managed-record count, last reconcile/change
  time, and per-zone `{adds, deletes, rcode, error}`.
- `GET /healthz` → 200 when healthy, 503 otherwise.

The packaged facter fact (`/opt/puppetlabs/facter/facts.d/dns_updater.sh`)
queries this and emits `dns_updater_healthy`, `dns_updater_zones_failed`,
`dns_updater_failed_zones`, etc. Puppet already knows the *desired* records (it
writes the file); these facts report what actually landed on the server.

## Logging

INFO on real changes (`applied`) and failures (`reconcile partial`,
`zone update failed` with zone + rcode); the steady-state "nothing to do" path
stays at DEBUG, so periodic resyncs and interface flaps do not spam the journal.
slog `key=value` output parses cleanly in VictoriaLogs.

## Build / release

```
make build      # binary
make test       # unit + in-process TSIG server integration tests
make rpm        # binaries + RPM into dist/ via a goreleaser snapshot
make patch      # tag vX.Y.(Z+1) and push -> GitHub release
```
