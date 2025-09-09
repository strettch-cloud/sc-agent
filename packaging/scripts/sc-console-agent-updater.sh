#!/bin/bash
# SC-Agent Auto-Update Script
# Purpose: Check for and install updates ONLY for sc-console-agent package
# Triggered by: sc-console-agent-update-scheduler.timer

set -euo pipefail

# Configuration
readonly PACKAGE_NAME="sc-console-agent"
readonly LOG_PREFIX="[SC-Agent-Updater]"
readonly LOCK_FILE="/var/run/sc-console-agent-updater.lock"
readonly MAX_RETRIES=3
readonly RETRY_DELAY=30

# Ensure we're running as root
if [ "$EUID" -ne 0 ]; then 
    echo "${LOG_PREFIX} Error: Must run as root" >&2
    exit 1
fi

# Logging function
log() {
    local level=$1
    shift
    local message="$@"
    local timestamp=$(date -u +"%Y-%m-%d %H:%M:%S UTC")
    
    # Use appropriate output stream
    case $level in
        ERROR|WARN)
            echo "${LOG_PREFIX} ${timestamp} [${level}] ${message}" >&2
            ;;
        *)
            echo "${LOG_PREFIX} ${timestamp} [${level}] ${message}"
            ;;
    esac
}

# Cleanup function
cleanup() {
    rm -f "${LOCK_FILE}"
}

# Set up exit trap
trap cleanup EXIT INT TERM

# Check if another instance is running
if [ -f "${LOCK_FILE}" ]; then
    # Check if the PID in lock file is still running
    if [ -r "${LOCK_FILE}" ]; then
        LOCK_PID=$(cat "${LOCK_FILE}" 2>/dev/null || echo "")
        if [ -n "${LOCK_PID}" ] && kill -0 "${LOCK_PID}" 2>/dev/null; then
            log "WARN" "Another updater instance is running (PID: ${LOCK_PID})"
            exit 0
        else
            log "INFO" "Removing stale lock file"
            rm -f "${LOCK_FILE}"
        fi
    fi
fi

# Create lock file
echo $$ > "${LOCK_FILE}"

# Start update process
log "INFO" "Starting update check for ${PACKAGE_NAME}"

# Check OS compatibility
if [ ! -f /etc/debian_version ]; then
    log "ERROR" "Not a Debian/Ubuntu system"
    exit 1
fi

# Add jitter to prevent thundering herd
JITTER=$((RANDOM % 60))
log "INFO" "Adding ${JITTER}s jitter delay"
sleep ${JITTER}

# Set APT environment
export DEBIAN_FRONTEND=noninteractive
export SC_AGENT_AUTO_UPDATER=1

# Function to check package health
check_package_health() {
    local status=$(dpkg -l ${PACKAGE_NAME} 2>/dev/null | grep "^[ih]" | awk '{print $1}')
    
    if [[ -z "$status" ]]; then
        log "ERROR" "Package not found in dpkg database"
        return 1
    elif [[ "$status" == "ii" ]]; then
        log "INFO" "Package health: OK (status: ii)"
        return 0
    elif [[ "$status" =~ [FRU] ]]; then
        log "WARN" "Package needs repair (status: ${status})"
        return 2
    else
        log "WARN" "Package status unknown: ${status}"
        return 2
    fi
}

# Function to repair package
repair_package() {
    log "INFO" "Attempting package repair..."
    
    # Try reconfiguration first
    if dpkg --configure ${PACKAGE_NAME} 2>&1; then
        log "INFO" "Package reconfiguration successful"
        return 0
    fi
    
    # Try reinstall
    log "INFO" "Reconfiguration failed, attempting reinstall..."
    if apt-get install --reinstall -y \
        -o DPkg::Lock::Timeout=300 \
        -o Dpkg::Options::="--force-confdef" \
        -o Dpkg::Options::="--force-confold" \
        ${PACKAGE_NAME} 2>&1; then
        log "INFO" "Package reinstall successful"
        return 0
    fi
    
    log "ERROR" "Package repair failed"
    return 1
}

# Check and repair package if needed
check_package_health
HEALTH_STATUS=$?

if [ ${HEALTH_STATUS} -eq 1 ]; then
    log "ERROR" "Package not installed, cannot update"
    exit 1
elif [ ${HEALTH_STATUS} -eq 2 ]; then
    repair_package || exit 1
fi

# Update package lists with retry
for attempt in $(seq 1 ${MAX_RETRIES}); do
    log "INFO" "Updating package lists (attempt ${attempt}/${MAX_RETRIES})"
    
    # Clean package cache for fresh data
    apt-get clean
    
    if apt-get update 2>&1; then
        log "INFO" "Package list update successful"
        break
    else
        if [ ${attempt} -lt ${MAX_RETRIES} ]; then
            log "WARN" "Package list update failed, retrying in ${RETRY_DELAY}s..."
            sleep ${RETRY_DELAY}
        else
            log "ERROR" "Package list update failed after ${MAX_RETRIES} attempts"
            exit 1
        fi
    fi
done

# Get version information
CURRENT_VERSION=$(dpkg-query -W -f='${Version}' ${PACKAGE_NAME} 2>/dev/null || echo "unknown")
log "INFO" "Current version: ${CURRENT_VERSION}"

# Check available version using apt-cache policy
AVAILABLE_VERSION=$(apt-cache policy ${PACKAGE_NAME} 2>/dev/null | grep "Candidate:" | awk '{print $2}' || echo "unknown")
log "INFO" "Available version: ${AVAILABLE_VERSION}"

# Verify we got valid version info
if [ "${AVAILABLE_VERSION}" = "unknown" ] || [ "${AVAILABLE_VERSION}" = "(none)" ]; then
    log "ERROR" "Could not determine available version"
    exit 1
fi

# Check if update is needed
if [ "${CURRENT_VERSION}" = "${AVAILABLE_VERSION}" ]; then
    log "INFO" "Already running latest version (${CURRENT_VERSION})"
    exit 0
fi

# Perform update
log "INFO" "Updating from ${CURRENT_VERSION} to ${AVAILABLE_VERSION}"

# Update with retry logic
UPDATE_SUCCESS=false
for attempt in $(seq 1 ${MAX_RETRIES}); do
    log "INFO" "Installation attempt ${attempt}/${MAX_RETRIES}"
    
    if apt-get install -y \
        -o DPkg::Lock::Timeout=300 \
        -o Dpkg::Options::="--force-confdef" \
        -o Dpkg::Options::="--force-confold" \
        -o APT::Get::AutomaticRemove=false \
        ${PACKAGE_NAME} 2>&1; then
        
        # Verify update
        NEW_VERSION=$(dpkg-query -W -f='${Version}' ${PACKAGE_NAME} 2>/dev/null || echo "unknown")
        
        if [ "${NEW_VERSION}" != "${CURRENT_VERSION}" ] && [ "${NEW_VERSION}" != "unknown" ]; then
            log "INFO" "Update successful: ${CURRENT_VERSION} -> ${NEW_VERSION}"
            UPDATE_SUCCESS=true
            break
        else
            log "ERROR" "Update verification failed (version unchanged)"
        fi
    else
        log "ERROR" "Installation attempt ${attempt} failed"
    fi
    
    if [ ${attempt} -lt ${MAX_RETRIES} ]; then
        log "INFO" "Retrying in ${RETRY_DELAY}s..."
        sleep ${RETRY_DELAY}
        
        # Try to fix any issues before retry
        dpkg --configure -a 2>&1 || true
        apt-get install -f -y 2>&1 || true
    fi
done

if [ "${UPDATE_SUCCESS}" = true ]; then
    # Check if service needs restart
    if systemctl is-active --quiet ${PACKAGE_NAME} 2>/dev/null; then
        log "INFO" "Restarting service to apply update..."
        
        # Give service time to shut down gracefully
        if systemctl restart ${PACKAGE_NAME} 2>&1; then
            sleep 2
            
            # Verify service is running
            if systemctl is-active --quiet ${PACKAGE_NAME} 2>/dev/null; then
                log "INFO" "Service restarted successfully"
            else
                log "ERROR" "Service failed to restart after update"
                log "INFO" "Attempting to start service..."
                systemctl start ${PACKAGE_NAME} 2>&1 || true
            fi
        else
            log "ERROR" "Failed to restart service"
        fi
    else
        log "INFO" "Service not running, skipping restart"
    fi
    
    log "INFO" "Update completed successfully"
    exit 0
else
    log "ERROR" "Update failed after ${MAX_RETRIES} attempts"
    
    # TODO: Send alert to monitoring system
    # Example: send_alert "SC-Agent update failed on $(hostname)"
    #  curl -X POST https://sc-monitoring.com/alert \
    #   -d "host=$(hostname)&service=sc-agent-updater&message=Update failed"
    
    exit 1
fi