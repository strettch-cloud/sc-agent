#!/bin/bash
# SC Agent External Updater
# This script handles updating the agent safely by stopping it first

set -e

PACKAGE_NAME="sc-agent"
SERVICE_NAME="${PACKAGE_NAME}.service"
CONFIG_FILE="/etc/${PACKAGE_NAME}/config.yaml"
LOCK_FILE="/var/lock/${PACKAGE_NAME}-updater.lock"

# Create lock file to prevent concurrent updates
if ! mkdir "${LOCK_FILE}" 2>/dev/null; then
    echo "ERROR: Another update is already in progress"
    exit 1
fi

# Cleanup function
cleanup() {
    rmdir "${LOCK_FILE}" 2>/dev/null || true
}
trap cleanup EXIT

echo "Starting SC Agent update..."

# Check if service is active before stopping
if systemctl is-active --quiet "${SERVICE_NAME}"; then
    echo "Stopping ${SERVICE_NAME}..."
    systemctl stop "${SERVICE_NAME}"
    SERVICE_WAS_RUNNING=true
else
    echo "${SERVICE_NAME} is not running"
    SERVICE_WAS_RUNNING=false
fi

# Update package
echo "Updating package..."
apt-get update -qq
apt-get install -y "${PACKAGE_NAME}"

# Validate new configuration (the binary should have --validate-config flag)
echo "Validating configuration..."
if ! "/usr/local/bin/${PACKAGE_NAME}" --validate-config "${CONFIG_FILE}"; then
    echo "ERROR: Configuration validation failed"
    echo "Update aborted - configuration is invalid"
    exit 1
fi

# Start service if it was running before
if [ "$SERVICE_WAS_RUNNING" = true ]; then
    echo "Starting ${SERVICE_NAME}..."
    systemctl start "${SERVICE_NAME}"
    
    # Wait a moment and check if it started successfully
    sleep 2
    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        echo "✅ Update completed successfully"
    else
        echo "WARNING: Service may not have started properly"
        echo "Check status with: systemctl status ${SERVICE_NAME}"
        exit 1
    fi
else
    echo "✅ Update completed successfully (service was not running)"
fi

echo "Update finished at $(date)"