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

# Add jitter to prevent thundering herd (0-300 seconds for 1-minute timer)
JITTER=$((RANDOM % 300))
echo "Adding jitter delay of ${JITTER} seconds to prevent server overload..."
sleep ${JITTER}

# Set non-interactive mode for APT operations
export DEBIAN_FRONTEND=noninteractive

# Fix any interrupted dpkg operations before attempting updates (retry logic)
echo "Checking for interrupted dpkg operations..."
for attempt in {1..3}; do
    if dpkg --audit >/dev/null 2>&1; then
        echo "dpkg state is clean"
        break
    else
        echo "Found interrupted dpkg operations (attempt $attempt/3), attempting to fix..."
        # Fix interrupted package installations
        if dpkg --configure -a 2>/dev/null; then
            echo "dpkg --configure -a completed successfully"
        else
            echo "Warning: dpkg --configure -a had issues, continuing..."
        fi
        # Clean up any broken package dependencies
        if apt-get install -f -y >/dev/null 2>&1; then
            echo "Dependency repair completed successfully"
        else
            echo "Warning: apt-get install -f had issues, continuing..."
        fi
    fi
    
    if [ $attempt -lt 3 ]; then
        echo "Waiting 10 seconds before retry..."
        sleep 10
    fi
done

# Download package lists with retry logic
echo "Updating package lists..."
for attempt in {1..3}; do
    if apt-get update >/dev/null 2>&1; then
        echo "Package list update successful"
        break
    else
        echo "Package list update failed (attempt $attempt/3)"
        if [ $attempt -lt 3 ]; then
            echo "Waiting 30 seconds before retry..."
            sleep 30
        else
            echo "Warning: All package list update attempts failed, continuing..."
        fi
    fi
done

# Check if update is actually needed
CURRENT_VERSION=$(dpkg-query -W -f='${Version}' sc-console-agent 2>/dev/null || echo "none")
AVAILABLE_VERSION=$(apt-cache policy sc-console-agent | grep Candidate: | awk '{print $2}' 2>/dev/null || echo "none")

echo "Current version: $CURRENT_VERSION"
echo "Available version: $AVAILABLE_VERSION"

if [ "$CURRENT_VERSION" = "$AVAILABLE_VERSION" ]; then
    echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Already running the latest version, no update needed"
    exit 0
fi

# Attempt to upgrade with retry logic
echo "Update available, attempting installation..."
for attempt in {1..3}; do
    echo "Installation attempt $attempt/3..."
    
    # Use --only-upgrade to prevent fresh installations, -o options for robustness  
    if apt-get install -y --only-upgrade \
        -o Dpkg::Options::="--force-confdef" \
        -o Dpkg::Options::="--force-confold" \
        sc-console-agent 2>/dev/null; then
        
        echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] SC-Agent update completed successfully"
        
        # Verify the update actually succeeded
        NEW_VERSION=$(dpkg-query -W -f='${Version}' sc-console-agent 2>/dev/null || echo "none")
        if [ "$NEW_VERSION" != "$CURRENT_VERSION" ]; then
            echo "Update verified: $CURRENT_VERSION -> $NEW_VERSION"
            
            # Smart service restart (only if service is actually running)
            if systemctl is-active --quiet sc-console-agent; then
                echo "Restarting SC-Agent to apply updates..."
                if systemctl restart sc-console-agent; then
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
            dpkg --configure -a 2>/dev/null || true
            apt-get install -f -y 2>/dev/null || true
        fi
    fi
done

echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Update process completed (check logs above for results)"

# Note: Exit code doesn't matter for systemd timer - it will run again in 2 hours