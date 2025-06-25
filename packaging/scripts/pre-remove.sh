#!/bin/sh

set -e

if [ "$1" = "purge" ]; then
    echo "Purging sc-agent..."

    systemctl stop sc-agent.service || true
    systemctl disable sc-agent.service || true

    rm -rf /etc/sc-agent
    rm -f /etc/systemd/system/sc-agent.service

    systemctl daemon-reload || true
    systemctl reset-failed sc-agent.service || true

    echo "sc-agent purge script finished."
else
    echo "Stopping and disabling sc-agent (on remove/upgrade)..."
    systemctl stop sc-agent.service || true
    systemctl disable sc-agent.service || true
fi

exit 0 