#!/bin/sh
set -e
if [ -d /run/systemd/system ] && [ "$1" = remove ]; then
    systemctl disable --now drivelist-agent.service >/dev/null 2>&1 || true
    systemctl disable --now drivelist-server.service >/dev/null 2>&1 || true
fi
