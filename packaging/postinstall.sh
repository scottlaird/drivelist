#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    # Enabled so it starts on boot; the unit's ConditionPathExists keeps it
    # inert until /etc/drivelist/agent-token exists.
    systemctl enable drivelist-agent.service >/dev/null 2>&1 || true
    if [ -f /etc/drivelist/agent-token ]; then
        systemctl restart drivelist-agent.service || true
    else
        echo "drivelist: put the agent token in /etc/drivelist/agent-token and the server in /etc/default/drivelist, then: systemctl start drivelist-agent"
    fi
    if systemctl is-active --quiet drivelist-server.service; then
        systemctl restart drivelist-server.service || true
    fi
fi
