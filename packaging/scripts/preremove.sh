#!/bin/sh
set -e
# $1 is 0 on final removal (rpm), "remove" on purge (deb).
if [ "$1" = "0" ] || [ "$1" = "remove" ] || [ "$1" = "purge" ]; then
    systemctl disable --now dns-updater.service >/dev/null 2>&1 || true
fi
