#!/bin/bash
# SC-Agent Pre-Removal Script
# Purpose: Clean up services and files before package removal
# Triggered by: APT package manager before removing/upgrading sc-console-agent
# Parameter $1: "purge" for complete removal, "remove" for keeping configs, "upgrade" for updates

set -e  # Exit immediately if any command fails

# Define service names that need to be stopped/disabled
SERVICE_NAME="sc-console-agent.service"
UPDATER_SERVICE="sc-console-agent-update-scheduler.service"
UPDATER_TIMER="sc-console-agent-update-scheduler.timer"

# Check the type of removal being performed
# "purge" = complete removal including configs (apt-get purge)
if [ "$1" = "purge" ] || [ "$1" = "0" ]; then
    echo "Purging SC-Agent (complete removal)..."
    
    # Stop all services and timers before removal
    # || true ensures script continues even if service doesn't exist
    echo "Stopping services..."
    systemctl stop ${UPDATER_TIMER} || true     # Stop the update timer first
    systemctl stop ${UPDATER_SERVICE} || true   # Stop any running update
    systemctl stop ${SERVICE_NAME} || true      # Stop the main agent
    
    # Disable all services so they don't start on boot
    echo "Disabling services..."
    systemctl disable ${UPDATER_TIMER} || true  # Remove timer from boot sequence
    systemctl disable ${UPDATER_SERVICE} || true # Remove updater from boot
    systemctl disable ${SERVICE_NAME} || true   # Remove main service from boot
    
    # Remove all configuration and runtime files (purge only)
    echo "Removing configuration and runtime files..."
    rm -rf /etc/sc-console-agent        # Delete all config files
    rm -rf /var/run/sc-console-agent    # Delete runtime/PID files
    
    # Remove systemd service files if they exist
    rm -f /etc/systemd/system/${SERVICE_NAME}
    rm -f /etc/systemd/system/${UPDATER_SERVICE}
    rm -f /etc/systemd/system/${UPDATER_TIMER}
    
    # Tell systemd to reload and clear any failed states
    systemctl daemon-reload || true                # Reload systemd configuration
    systemctl reset-failed ${SERVICE_NAME} || true # Clear failed state
    systemctl reset-failed ${UPDATER_SERVICE} || true # Clear updater failed state
    
    echo "SC-Agent purge completed."
else
    # Regular removal (apt-get remove) or upgrade (apt-get upgrade)
    echo "Stopping SC-Agent services..."
    
    # Stop services but don't delete config files
    # This allows for reinstallation without losing configuration
    
    # Check if we're being called from the auto-updater
    if systemctl is-active --quiet ${UPDATER_SERVICE}; then
        echo "Auto-updater is running, skipping updater service stop"
        systemctl stop ${UPDATER_TIMER} || true
        systemctl stop ${SERVICE_NAME} || true
    else
        # Normal upgrade/removal - stop all services
        systemctl stop ${UPDATER_TIMER} || true
        systemctl stop ${UPDATER_SERVICE} || true
        systemctl stop ${SERVICE_NAME} || true
    fi
    
    # Only disable services on removal, not during upgrade
    if [ "$1" = "remove" ]; then
        # User is removing but not purging - disable services but keep configs
        echo "Disabling SC-Agent services..."
        systemctl disable ${UPDATER_TIMER} || true
        systemctl disable ${UPDATER_SERVICE} || true
        systemctl disable ${SERVICE_NAME} || true
    fi
    # If $1 is "upgrade", services stay enabled and will restart after upgrade
fi

exit 0