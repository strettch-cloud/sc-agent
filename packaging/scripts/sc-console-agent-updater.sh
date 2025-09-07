#!/bin/bash
# SC-Agent Auto-Update Script
# Purpose: Check for and install updates ONLY for sc-console-agent package
# Triggered by: sc-console-agent-update-scheduler.timer every 2 hours
#

set -e

# Package name to update - change this if the package name changes
PACKAGE_NAME="sc-console-agent"

# Check if running on Debian/Ubuntu (defensive check)
if [ ! -f /etc/debian_version ]; then
    echo "Error: Auto-updater only supports Debian/Ubuntu systems"
    exit 1
fi

echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Starting SC-Agent update check..."

# Add jitter to prevent thundering herd (0-30 seconds for 1-minute timer)
JITTER=$((RANDOM % 30))
echo "Adding jitter delay of ${JITTER} seconds to prevent server overload..."
sleep ${JITTER}

# Set non-interactive mode for APT operations
export DEBIAN_FRONTEND=noninteractive

# Signal that auto-updater is running
export SC_AGENT_AUTO_UPDATER=1

# Only check if OUR package has issues, don't fix system-wide problems
echo "Checking ${PACKAGE_NAME} package status..."
PACKAGE_STATUS=$(dpkg -l ${PACKAGE_NAME} 2>/dev/null | grep "^[ih]" | awk '{print $1}')
if [[ "$PACKAGE_STATUS" == *"F"* ]] || [[ "$PACKAGE_STATUS" == *"R"* ]] || [[ "$PACKAGE_STATUS" == *"U"* ]]; then
    echo "Warning: ${PACKAGE_NAME} has configuration issues (status: $PACKAGE_STATUS)"
    echo "Will attempt to fix during installation..."
fi

# Download package lists with retry logic and cache clearing
echo "Updating package lists..."
for attempt in {1..3}; do
    # Clear APT cache to ensure fresh data
    apt-get clean
    if apt-get update 2>&1; then
        echo "Package list update successful"
        # Give APT cache a moment to settle
        sleep 2
        break
    else
        echo "Package list update failed (attempt $attempt/3)"
        if [ $attempt -lt 3 ]; then
            echo "Waiting 30 seconds before retry..."
            sleep 30
        else
            echo "Error: Cannot check for updates without package list refresh"
            echo "This does not affect other packages, only prevents us from checking for updates"
            exit 1
        fi
    fi
done

# Comprehensive package state checking and repair
echo "Performing comprehensive package state check..."
PACKAGE_STATUS=$(dpkg -l ${PACKAGE_NAME} 2>/dev/null | grep "^[ih]" | awk '{print $1}')

# Check for various broken states
if [[ "$PACKAGE_STATUS" == *"F"* ]] || [[ "$PACKAGE_STATUS" == *"R"* ]] || [[ "$PACKAGE_STATUS" == *"U"* ]]; then
    echo "${PACKAGE_NAME} has configuration issues (status: $PACKAGE_STATUS)"
    
    # Try multiple repair strategies
    echo "Strategy 1: Attempting to reconfigure package..."
    if dpkg --configure ${PACKAGE_NAME} 2>&1; then
        echo "Package reconfiguration successful"
    else
        echo "Package reconfiguration failed, trying alternative approaches..."
        
        echo "Strategy 2: Attempting to reinstall current version..."
        if apt-get install --reinstall -y ${PACKAGE_NAME} 2>&1; then
            echo "Package reinstallation successful"
        else
            echo "Reinstallation failed, will force complete reinstall during update"
            FORCE_REINSTALL=1
        fi
    fi
    
    # Verify repair was successful
    PACKAGE_STATUS_AFTER=$(dpkg -l ${PACKAGE_NAME} 2>/dev/null | grep "^[ih]" | awk '{print $1}')
    if [[ "$PACKAGE_STATUS_AFTER" == "ii" ]]; then
        echo "Package state successfully repaired: $PACKAGE_STATUS -> $PACKAGE_STATUS_AFTER"
    else
        echo "Package state still problematic: $PACKAGE_STATUS -> $PACKAGE_STATUS_AFTER"
        echo "Will attempt complete reinstall during update process"
        FORCE_REINSTALL=1
    fi
elif [[ "$PACKAGE_STATUS" == "ii" ]]; then
    echo "Package state is healthy (ii - installed and configured)"
elif [[ -z "$PACKAGE_STATUS" ]]; then
    echo "Warning: Package not found in dpkg database, may need fresh installation"
    FORCE_REINSTALL=1
else
    echo "Package status: $PACKAGE_STATUS (proceeding with caution)"
fi

# Check if update is actually needed with double verification
CURRENT_VERSION=$(dpkg-query -W -f='${Version}' ${PACKAGE_NAME} 2>/dev/null || echo "none")

# Get available version with multiple methods for reliability
AVAILABLE_VERSION=$(apt-cache policy ${PACKAGE_NAME} | grep Candidate: | awk '{print $2}' 2>/dev/null || echo "none")
AVAILABLE_VERSION_ALT=$(apt list --upgradable ${PACKAGE_NAME} 2>/dev/null | grep ${PACKAGE_NAME} | cut -d' ' -f2 2>/dev/null || echo "none")

echo "Current version: $CURRENT_VERSION"
echo "Available version (policy): $AVAILABLE_VERSION"
echo "Available version (list): $AVAILABLE_VERSION_ALT"

# Use the newer version if they differ
if [ "$AVAILABLE_VERSION_ALT" != "none" ] && [ "$AVAILABLE_VERSION_ALT" != "$AVAILABLE_VERSION" ]; then
    echo "Using alternate method result: $AVAILABLE_VERSION_ALT"
    AVAILABLE_VERSION="$AVAILABLE_VERSION_ALT"
fi

if [ "$CURRENT_VERSION" = "$AVAILABLE_VERSION" ] || [ "$AVAILABLE_VERSION" = "none" ]; then
    echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Already running the latest version, no update needed"
    exit 0
fi

# Attempt to upgrade with retry logic
echo "Update available, attempting installation..."
for attempt in {1..3}; do
    echo "Installation attempt $attempt/3..."
    
    # Determine if we need reinstall or just upgrade
    if [ "${FORCE_REINSTALL}" = "1" ]; then
        echo "Using reinstall due to package configuration issues..."
        INSTALL_CMD="apt-get install --reinstall -y"
    else
        INSTALL_CMD="apt-get install -y"
    fi
    
    # ONLY install/upgrade ${PACKAGE_NAME}, nothing else
    if $INSTALL_CMD \
        -o Dpkg::Options::="--force-confdef" \
        -o Dpkg::Options::="--force-confold" \
        ${PACKAGE_NAME} 2>&1; then
        
        echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] SC-Agent update completed successfully"
        
        # Verify the update actually succeeded
        NEW_VERSION=$(dpkg-query -W -f='${Version}' ${PACKAGE_NAME} 2>/dev/null || echo "none")
        if [ "$NEW_VERSION" != "$CURRENT_VERSION" ]; then
            echo "Update verified: $CURRENT_VERSION -> $NEW_VERSION"
            
            # Smart service restart (only if service is actually running)
            if systemctl is-active --quiet ${PACKAGE_NAME}; then
                echo "Restarting SC-Agent to apply updates..."
                if systemctl restart ${PACKAGE_NAME}; then
                    echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] SC-Agent restarted successfully"
                else
                    echo "Warning: Service restart failed, but update was successful"
                fi
            else
                echo "Service not running, skipping restart"
            fi
            exit 0
        else
            echo "Warning: Update appeared successful but version didn't change"
        fi
        break
    else
        echo "Installation attempt $attempt failed"
        if [ $attempt -lt 3 ]; then
            echo "Waiting 30 seconds before retry..."
            sleep 30
            # Try to fix any issues before retry
            dpkg --configure ${PACKAGE_NAME} 2>&1 || true
            # Try to reinstall just our package
            apt-get install --reinstall -y ${PACKAGE_NAME} 2>&1 || true
        fi
    fi
done

echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Update process completed (check logs above for results)"

# Note: Exit code doesn't matter for systemd timer - it will run again in 2 hours