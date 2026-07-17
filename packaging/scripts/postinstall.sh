#!/bin/sh
set -e
systemctl daemon-reload >/dev/null 2>&1 || true
# Restart if already enabled/running; otherwise leave it for puppet to enable
# once the TSIG key and records file are in place.
if systemctl is-enabled dns-updater.service >/dev/null 2>&1; then
    systemctl try-restart dns-updater.service >/dev/null 2>&1 || true
fi
