#!/bin/bash
# SC-Agent Post-Installation Script
# Purpose: Configure directories, permissions, and start services after package installation
# Triggered by: APT package manager automatically after installing sc-agent.deb
# Note: This script runs as root during package installation

set -e  # Exit immediately if any command fails

# Define service names used throughout the script
SERVICE_NAME="sc-agent.service"          # Main agent service
UPDATER_SERVICE="sc-agent-updater.service"  # One-shot updater service
UPDATER_TIMER="sc-agent-update-scheduler.timer"      # Timer that triggers updates

echo "Configuring SC-Agent..."

# Create necessary directories for agent operation
# mkdir -p: create parent directories as needed, no error if exists
echo "Creating required directories..."
mkdir -p /var/run/sc-agent      # Directory for runtime files (PID, sockets)
mkdir -p /etc/sc-agent          # Directory for configuration files

# Set secure permissions on directories
# 750 = rwxr-x--- (owner: read/write/execute, group: read/execute, others: none)
# 755 = rwxr-xr-x (owner: read/write/execute, group & others: read/execute)
chmod 750 /var/run/sc-agent     # Runtime files should be private
chmod 755 /etc/sc-agent         # Config can be world-readable

# Reload systemd to recognize new service files that package installed
echo "Reloading systemd configuration..."
systemctl daemon-reload

# Enable main service to start on boot
# is-enabled: check if service is enabled without output
# --quiet: suppress output
if ! systemctl is-enabled --quiet "${SERVICE_NAME}"; then
    echo "Enabling ${SERVICE_NAME}..."
    systemctl enable "${SERVICE_NAME}"  # Create symlinks for boot startup
else
    echo "${SERVICE_NAME} is already enabled."
fi

# Enable auto-update timer to run periodically
if ! systemctl is-enabled --quiet "${UPDATER_TIMER}"; then
    echo "Enabling auto-update timer..."
    systemctl enable "${UPDATER_TIMER}"  # Enable timer for periodic updates
else
    echo "${UPDATER_TIMER} is already enabled."
fi

# Clear any previous failed states from systemd
# || true: don't fail if service doesn't exist or command fails
systemctl reset-failed "${SERVICE_NAME}" || true
systemctl reset-failed "${UPDATER_SERVICE}" || true

# Start or restart the main service
# is-active: check if service is currently running
if ! systemctl is-active --quiet "${SERVICE_NAME}"; then
    # Service not running - start it
    echo "Starting ${SERVICE_NAME}..."
    if systemctl start "${SERVICE_NAME}"; then
        echo "✅ ${SERVICE_NAME} started successfully"
    else
        # Service failed to start - provide debugging help
        echo "⚠️  Warning: ${SERVICE_NAME} could not be started immediately."
        echo "   Please check 'systemctl status ${SERVICE_NAME}' for details."
        echo "   View logs with: journalctl -u ${SERVICE_NAME} -f"
    fi
else
    # Service already running - restart to load new version
    echo "Restarting ${SERVICE_NAME} to apply updates..."
    systemctl restart "${SERVICE_NAME}"
fi

# Start the auto-update timer
if ! systemctl is-active --quiet "${UPDATER_TIMER}"; then
    echo "Starting auto-update timer..."
    systemctl start "${UPDATER_TIMER}" || true  # Don't fail if timer can't start
fi

# Print helpful status summary for the user
echo ""
echo "================================"
echo "SC-Agent Installation Complete"
echo "================================"
echo "Service status: $(systemctl is-active ${SERVICE_NAME})"  # Show running/dead status
echo "Auto-update: $(systemctl is-active ${UPDATER_TIMER})"   # Show timer active status
echo ""
echo "Useful commands:"
echo "  Check status:  systemctl status sc-agent"
echo "  View logs:     journalctl -u sc-agent -f"
echo "  Configuration: /etc/sc-agent/config.yaml"
echo "  Manual update: systemctl start sc-agent-updater"
echo ""