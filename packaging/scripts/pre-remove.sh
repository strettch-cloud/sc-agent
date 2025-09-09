#!/bin/bash
# SC-Agent Pre-Removal Script
# Purpose: Clean up services and files before package removal
# Triggered by: APT package manager before removing/upgrading sc-console-agent

set -euo pipefail

# Define service names
readonly SERVICE_NAME="sc-console-agent.service"
readonly UPDATER_SERVICE="sc-console-agent-update-scheduler.service" 
readonly UPDATER_TIMER="sc-console-agent-update-scheduler.timer"
readonly CONFIG_DIR="/etc/sc-console-agent"
readonly RUNTIME_DIR="/var/run/sc-console-agent"

# Get removal action from dpkg
ACTION="${1:-}"

# Helper function to stop service safely
stop_service() {
    local service=$1
    if systemctl is-active --quiet "$service" 2>/dev/null; then
        echo "Stopping $service..."
        systemctl stop "$service" 2>/dev/null || true
    fi
}

# Helper function to disable service safely
disable_service() {
    local service=$1
    if systemctl is-enabled --quiet "$service" 2>/dev/null; then
        echo "Disabling $service..."
        systemctl disable "$service" 2>/dev/null || true
    fi
}

# Main logic based on action
case "$ACTION" in
    purge|0)
        echo "Purging SC-Agent (complete removal)..."
        
        # Stop all services
        stop_service "${UPDATER_TIMER}"
        stop_service "${UPDATER_SERVICE}"
        stop_service "${SERVICE_NAME}"
        
        # Disable all services
        disable_service "${UPDATER_TIMER}"
        disable_service "${UPDATER_SERVICE}"
        disable_service "${SERVICE_NAME}"
        
        # Remove all files - no backup, user wants complete removal
        echo "Removing all configuration and runtime files..."
        rm -rf "$CONFIG_DIR"
        rm -rf "$RUNTIME_DIR"
        
        # Clean up systemd files
        find /etc/systemd/system -name "sc-console-agent*" -delete 2>/dev/null || true
        
        # Reload systemd
        systemctl daemon-reload 2>/dev/null || true
        
        echo "SC-Agent purge completed."
        ;;
        
    remove)
        echo "Removing SC-Agent (keeping configuration)..."
        
        # Stop and disable services
        stop_service "${UPDATER_TIMER}"
        stop_service "${UPDATER_SERVICE}"
        stop_service "${SERVICE_NAME}"
        
        disable_service "${UPDATER_TIMER}"
        disable_service "${UPDATER_SERVICE}"
        disable_service "${SERVICE_NAME}"
        
        # Only remove runtime files, keep config
        rm -rf "$RUNTIME_DIR"
        
        echo "SC-Agent removed (configuration preserved in $CONFIG_DIR)"
        ;;
        
    upgrade|1)
        echo "Preparing for SC-Agent upgrade..."
        
        # Check if auto-updater is running this
        if [ "${SC_AGENT_AUTO_UPDATER:-0}" = "1" ]; then
            echo "Auto-updater detected, minimal service disruption..."
            stop_service "${SERVICE_NAME}"
        else
            echo "Manual upgrade, stopping all services..."
            stop_service "${UPDATER_TIMER}"
            stop_service "${UPDATER_SERVICE}"
            stop_service "${SERVICE_NAME}"
        fi
        
        # Services remain enabled for restart after upgrade
        ;;
        
    *)
        echo "Unknown action: $ACTION (proceeding with standard removal)"
        stop_service "${UPDATER_TIMER}"
        stop_service "${UPDATER_SERVICE}"
        stop_service "${SERVICE_NAME}"
        ;;
esac

exit 0