#!/bin/bash
# SC-Agent Auto-Update Script
# Purpose: Check for and install updates via APT package manager
# Triggered by: sc-console-agent-update-scheduler.timer every 2 hours
# Note: Runs as root via systemd

set -e

# Check if running on Debian/Ubuntu (defensive check)
if [ ! -f /etc/debian_version ]; then
    echo "Error: Auto-updater only supports Debian/Ubuntu systems"
    exit 1
fi

echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Starting SC-Agent update check..."

# This downloads the latest package lists from repo.cloud.strettch.dev
apt-get update

# Attempt to upgrade only the sc-console-agent package
# --only-upgrade: Only upgrade if package is already installed (don't install new)
# -y: Automatic yes to prompts
if apt-get install -y --only-upgrade sc-console-agent; then
    echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] SC-Agent update check completed"
    
    # Check if service needs restart after update
    # Note: APT usually handles this via post-install script, but we double-check
    if systemctl is-active --quiet sc-console-agent; then
        # Service is running - restart to ensure new version is loaded
        echo "Restarting SC-Agent to apply updates..."
        systemctl restart sc-console-agent
        echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] SC-Agent restarted successfully"
    fi
else
    echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] No updates available or update failed"
fi

# Note: Exit code doesn't matter for systemd timer - it will run again in 2 hours