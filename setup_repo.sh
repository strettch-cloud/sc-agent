#!/bin/bash
# Repository Setup Script
# Purpose: Build packages and set up APT repository on the repository server
# Usage: ./setup_repo.sh (run on repository server)
# Location: This script runs on the repository server at /root/sc-console-agent/

set -e

# Enhanced error handling
trap 'echo "❌ Error occurred in setup_repo.sh at line $LINENO. Exit code: $?" >&2' ERR

# --- Configuration ---
ORG_NAME="strettch"
GPG_EMAIL="engineering@strettch.com"
PACKAGE_NAME="sc-console-agent"
KEEP_VERSIONS=5  # Number of versions to keep in repository

# Release type configuration (stable or beta)
RELEASE_TYPE="${RELEASE_TYPE:-stable}"
echo "Release type: $RELEASE_TYPE"

# Get the package version to use
if [ -n "$PACKAGE_VERSION" ]; then
    # Use provided version (from environment variable)
    echo "Using provided version: $PACKAGE_VERSION"
elif [ "$RELEASE_TYPE" = "stable" ]; then
    # For stable releases, only use stable version tags (not beta)
    LATEST_TAG=$(git tag -l --sort=-version:refname | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")
    if [ -n "$LATEST_TAG" ]; then
        PACKAGE_VERSION="${LATEST_TAG}"
        echo "Using latest stable version: $PACKAGE_VERSION"
    else
        echo "No stable version tags found, using default version"
        PACKAGE_VERSION="0.1.0"
    fi
else
    # For beta releases, get the latest tag (could be beta or stable)
    LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    if [ -n "$LATEST_TAG" ]; then
        PACKAGE_VERSION="${LATEST_TAG}"
        echo "Using version from latest tag: $PACKAGE_VERSION"
    else
        # Fallback to git describe if no tags
        PACKAGE_VERSION=$(git describe --tags --always --dirty)
        echo "No tags found, using git describe: $PACKAGE_VERSION"
    fi
fi

# Clean up any old packages
rm -f ${PACKAGE_NAME}_*.deb

REPO_DOMAIN="repo.cloud.strettch.com"  # Production repository domain
DISTRIBUTIONS="bionic focal jammy noble"  # Ubuntu versions to support

# Set web root directory based on release type
if [ "$RELEASE_TYPE" = "beta" ]; then
    WEB_ROOT_DIR="/srv/repo/public/console/beta"
    REPO_URL_PATH="console/beta"
else
    WEB_ROOT_DIR="/srv/repo/public/console"
    REPO_URL_PATH="console"
fi

echo "Web root directory: $WEB_ROOT_DIR"
echo "Repository URL path: $REPO_URL_PATH"
# ---

# --- Asset Paths ---
SERVICE_FILE="packaging/systemd/${PACKAGE_NAME}.service"
UPDATER_SERVICE="packaging/systemd/${PACKAGE_NAME}-update-scheduler.service"
UPDATER_TIMER="packaging/systemd/${PACKAGE_NAME}-update-scheduler.timer"
UPDATER_SCRIPT="packaging/scripts/${PACKAGE_NAME}-updater.sh"
POSTINSTALL_SCRIPT="packaging/scripts/post-install.sh"
PREREMOVE_SCRIPT="packaging/scripts/pre-remove.sh"
# ---

# Check for required tools first
for tool in fpm aptly gpg; do
    if ! command -v $tool &> /dev/null; then
        echo "Error: $tool is not installed" >&2
        exit 1
    fi
done

echo "--- [Step 1/7] Cleaning up previous repository publications..."

# Determine which repository and namespace we're working with
if [ "$RELEASE_TYPE" = "beta" ]; then
    REPO_NAME="sc-console-agent-beta-repo"
    APTLY_NAMESPACE="console-beta"
else
    REPO_NAME="sc-console-agent-stable-repo"
    APTLY_NAMESPACE="console"
fi

# Clean up any orphaned publications and snapshots from failed runs
echo "Cleaning up orphaned aptly state for $RELEASE_TYPE..."

# Check for broken publications and clean them up
echo "Checking for broken publications in ${APTLY_NAMESPACE}..."
for dist in ${DISTRIBUTIONS}; do
    if sudo aptly publish show "${dist}" "${APTLY_NAMESPACE}" >/dev/null 2>&1; then
        # Check if the publication is healthy
        if ! sudo aptly publish show "${dist}" "${APTLY_NAMESPACE}" | grep -q "Snapshot:"; then
            echo "Found broken publication for ${dist}, removing..."
            sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" -force-drop 2>/dev/null || true
        fi
    fi
done

# Clean up any old packages to keep repository clean
rm -f ${PACKAGE_NAME}_*.deb

echo "--- [Step 2/7] Building Go binary via Makefile..."
# Force VERSION to use the clean tag version
GOOS=linux GOARCH=amd64 make build VERSION=${PACKAGE_VERSION}

# --- Pre-flight Checks ---
echo "Checking required files..."
required_files=(
    "$SERVICE_FILE"
    "$UPDATER_SERVICE"
    "$UPDATER_TIMER"
    "$UPDATER_SCRIPT"
    "$POSTINSTALL_SCRIPT"
    "$PREREMOVE_SCRIPT"
    "build/${PACKAGE_NAME}"
)

for file in "${required_files[@]}"; do
    if [ ! -f "$file" ]; then
        echo "Error: Required file not found: ${file}" >&2
        echo "Run 'make build' first if binary is missing" >&2
        exit 1
    fi
done
# ---

echo "--- [Step 3/7] Preparing packaging assets..."
STAGING_DIR="/tmp/${PACKAGE_NAME}-build"
rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}/usr/bin" "${STAGING_DIR}/etc/systemd/system"

# Copy files
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"

cp "${UPDATER_SCRIPT}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"

cp "${SERVICE_FILE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_SERVICE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_TIMER}" "${STAGING_DIR}/etc/systemd/system/"

# Build .deb package with FPM
fpm -s dir -t deb \
    -n ${PACKAGE_NAME} \
    -v ${PACKAGE_VERSION} \
    -C "${STAGING_DIR}/" \
    --description "SC Agent for SSH access management by ${ORG_NAME}" \
    --maintainer "${GPG_EMAIL}" \
    --url "https://github.com/strettch/sc-console-agent" \
    --depends "libc6" --depends "dmidecode" --depends "jq" --depends "curl" \
    --after-install "${POSTINSTALL_SCRIPT}" \
    --before-remove "${PREREMOVE_SCRIPT}"

echo "--- [Step 4/7] Adding package to APT repository..."

# Use separate repositories for stable and beta to prevent conflicts
if [ "$RELEASE_TYPE" = "beta" ]; then
    REPO_NAME="sc-console-agent-beta-repo"
else
    REPO_NAME="sc-console-agent-stable-repo"
fi

echo "Using repository: $REPO_NAME for release type: $RELEASE_TYPE"

# Create the appropriate repository if it doesn't exist
if ! sudo aptly repo show "$REPO_NAME" > /dev/null 2>&1; then
    echo "Creating new repository: $REPO_NAME"
    sudo aptly repo create -distribution="focal" -component="main" "$REPO_NAME"
fi

# Remove any existing package with the same name/version to prevent conflicts
PACKAGE_FULL_NAME="${PACKAGE_NAME}_${PACKAGE_VERSION}_amd64"
echo "Checking for existing package: $PACKAGE_FULL_NAME in $REPO_NAME"
if sudo aptly repo show -with-packages "$REPO_NAME" 2>/dev/null | grep -q "^  ${PACKAGE_FULL_NAME}$"; then
    echo "Removing existing conflicting package: $PACKAGE_FULL_NAME"
    sudo aptly repo remove "$REPO_NAME" "$PACKAGE_FULL_NAME" || true
fi

# Clean up old versions before adding new one (only within the same release type)
echo "Checking for old versions to remove (keeping last ${KEEP_VERSIONS} versions)..."
CURRENT_VERSIONS=$(sudo aptly repo show -with-packages "$REPO_NAME" 2>/dev/null | grep "^  ${PACKAGE_NAME}_" | sed 's/^  //' | sort -V)
VERSION_COUNT=$(echo "$CURRENT_VERSIONS" | grep -c . || echo 0)

if [ $VERSION_COUNT -ge $KEEP_VERSIONS ]; then
    VERSIONS_TO_REMOVE=$((VERSION_COUNT - KEEP_VERSIONS + 1))
    echo "Found $VERSION_COUNT versions, removing $VERSIONS_TO_REMOVE oldest versions..."
    
    # Get the versions to remove (oldest ones)
    OLD_VERSIONS=$(echo "$CURRENT_VERSIONS" | head -n $VERSIONS_TO_REMOVE)
    
    for old_ver in $OLD_VERSIONS; do
        echo "Removing old version: $old_ver"
        sudo aptly repo remove "$REPO_NAME" "$old_ver" || true
    done
fi

sudo aptly repo add "$REPO_NAME" ${PACKAGE_NAME}_${PACKAGE_VERSION}_amd64.deb
SNAPSHOT_NAME="${PACKAGE_NAME}-${PACKAGE_VERSION}"

# Handle existing snapshots with the new repository-specific approach
echo "Creating snapshot: ${SNAPSHOT_NAME} from repository: $REPO_NAME"
if sudo aptly snapshot show "${SNAPSHOT_NAME}" >/dev/null 2>&1; then
    echo "Snapshot ${SNAPSHOT_NAME} already exists, removing it..."
    # Drop any publications using this snapshot first
    for dist in ${DISTRIBUTIONS}; do
        sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" 2>/dev/null || true
    done
    # Now drop the snapshot
    sudo aptly snapshot drop "${SNAPSHOT_NAME}" -force
fi

sudo aptly snapshot create "${SNAPSHOT_NAME}" from repo "$REPO_NAME"

# Clean up old snapshots for this release type (keep last 10 snapshots)
echo "Cleaning up old snapshots for release type: $RELEASE_TYPE..."
if [ "$RELEASE_TYPE" = "beta" ]; then
    # For beta, clean up beta snapshots
    OLD_SNAPSHOTS=$(sudo aptly snapshot list | grep "^${PACKAGE_NAME}-.*-beta\." | sort -V | head -n -10)
else
    # For stable, clean up stable snapshots (no beta in name)
    OLD_SNAPSHOTS=$(sudo aptly snapshot list | grep "^${PACKAGE_NAME}-" | grep -v "beta" | sort -V | head -n -10)
fi

if [ -n "$OLD_SNAPSHOTS" ]; then
    echo "Removing old snapshots:"
    echo "$OLD_SNAPSHOTS"
    for old_snapshot in $OLD_SNAPSHOTS; do
        echo "Removing old snapshot: $old_snapshot"
        sudo aptly snapshot drop "$old_snapshot" -force 2>/dev/null || true
    done
else
    echo "No old snapshots to clean up for $RELEASE_TYPE"
fi

# --- Configure GPG environment ---
export GPG_TTY=$(tty)
unset DISPLAY

# Get the fingerprint for the GPG key
GPG_FINGERPRINT=$(gpg --list-secret-keys --with-colons "${GPG_EMAIL}" | awk -F: '/^sec/{print $5; exit}')
if [ -z "${GPG_FINGERPRINT}" ]; then
    echo "Error: Could not find GPG key for ${GPG_EMAIL}" >&2
    exit 1
fi

# Publish snapshots with correct fingerprint to separate namespace
# APTLY_NAMESPACE already set at the beginning of the script

for dist in ${DISTRIBUTIONS}; do
    echo "Publishing for ${dist} to namespace ${APTLY_NAMESPACE}..."
    # Drop any existing publication first to avoid conflicts
    sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" 2>/dev/null || true
    sudo aptly publish snapshot -gpg-key="${GPG_FINGERPRINT}" -distribution="${dist}" -batch "${SNAPSHOT_NAME}" "${APTLY_NAMESPACE}"
done

echo "--- [Step 5/7] Creating and Publishing the new version..."
sudo mkdir -p "${WEB_ROOT_DIR}/aptly"
# Copy only the specific namespace to avoid mixing repositories
if [ -d ~/.aptly/public/${APTLY_NAMESPACE} ]; then
    sudo rsync -a --delete ~/.aptly/public/${APTLY_NAMESPACE}/ "${WEB_ROOT_DIR}/aptly/"
    echo "✅ Copied aptly namespace '${APTLY_NAMESPACE}' to web directory"
else
    echo "Warning: Aptly namespace ${APTLY_NAMESPACE} not found, copying entire public directory"
    sudo rsync -a --delete ~/.aptly/public/ "${WEB_ROOT_DIR}/aptly/"
fi

# Export GPG public key
GPG_PUBLIC_KEY_FILE="${PACKAGE_NAME}-repo.gpg"
gpg --armor --export "${GPG_EMAIL}" > "${GPG_PUBLIC_KEY_FILE}"
sudo mv "${GPG_PUBLIC_KEY_FILE}" "${WEB_ROOT_DIR}/"

# Generate dynamic install.sh
INSTALL_SCRIPT_PATH="${WEB_ROOT_DIR}/install.sh"
TMP_INSTALL_SCRIPT=$(mktemp)
cat << 'INSTALL_EOF' > "${TMP_INSTALL_SCRIPT}"
#!/bin/sh
set -e
REPO_HOST="repo.cloud.strettch.com"
REPO_PATH="REPO_URL_PATH_PLACEHOLDER"
PACKAGE_NAME="sc-console-agent"
GPG_KEY_FILENAME="sc-console-agent-repo.gpg"
CONFIG_FILE="/etc/sc-console-agent/config.yaml"

if [ "$(id -u)" -ne "0" ]; then
    echo "Run as root" >&2
    exit 1
fi

if [ -f /etc/os-release ]; then
    DISTRIBUTION=$(. /etc/os-release && echo "$VERSION_CODENAME")
else
    echo "Error: Cannot detect OS codename" >&2
    exit 1
fi

# Fallback logic for unsupported distributions including EOL ones
SUPPORTED_DISTS="bionic focal jammy noble"
if ! echo "$SUPPORTED_DISTS" | grep -q "$DISTRIBUTION"; then
    # Special handling for oracular (24.10) which is EOL
    if [ "$DISTRIBUTION" = "oracular" ]; then
        echo "Note: Ubuntu 24.10 (oracular) has reached end-of-life. Using noble (24.04 LTS) repository."
        DISTRIBUTION="noble"
    else
        echo "Warning: $DISTRIBUTION is not explicitly supported, falling back to noble (Ubuntu 24.04)"
        DISTRIBUTION="noble"
    fi
fi

echo "--- Installing ${PACKAGE_NAME} for ${DISTRIBUTION} ---"

log_and_run() {
    echo "$1"
    if ! eval "$2"; then
        echo "Error: Failed to $3" >&2
        exit 1
    fi
}

# Clean up any old sc-agent configurations
echo "Cleaning up old configurations..."
rm -f /etc/apt/sources.list.d/sc-agent.list
rm -f /usr/share/keyrings/sc-agent-keyring.gpg
rm -f /etc/apt/sources.list.d/sc-console-agent.list 2>/dev/null || true
rm -f /usr/share/keyrings/sc-console-agent-keyring.gpg 2>/dev/null || true

log_and_run "Updating package lists..." "apt-get update > /dev/null" "update package lists"
log_and_run "Installing prerequisites..." "apt-get install -y apt-transport-https ca-certificates curl gnupg > /dev/null" "install prerequisites"

KEYRING_FILE="/usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg"
log_and_run "Downloading GPG key..." "curl -fsSL \"https://${REPO_HOST}/${REPO_PATH}/${GPG_KEY_FILENAME}\" | gpg --dearmor | tee \"${KEYRING_FILE}\" >/dev/null" "download GPG key"

SOURCES_FILE="/etc/apt/sources.list.d/${PACKAGE_NAME}.list"
log_and_run "Adding repository..." "echo \"deb [signed-by=${KEYRING_FILE}] https://${REPO_HOST}/${REPO_PATH}/aptly ${DISTRIBUTION} main\" | tee \"${SOURCES_FILE}\" >/dev/null" "add repository"

log_and_run "Updating package index..." "apt-get update -o Dir::Etc::SourceList='${SOURCES_FILE}' -o Dir::Etc::SourceParts='-' -o APT::Get::List-Cleanup='0' > /dev/null" "update package index"

log_and_run "Installing ${PACKAGE_NAME}..." "apt-get install -y \"${PACKAGE_NAME}\"" "install ${PACKAGE_NAME}"

echo "✅ ${PACKAGE_NAME} installed successfully!"
exit 0
INSTALL_EOF

# Replace placeholder with actual value
sed -i "s|REPO_URL_PATH_PLACEHOLDER|${REPO_URL_PATH}|g" "${TMP_INSTALL_SCRIPT}"

sudo mv "${TMP_INSTALL_SCRIPT}" "${INSTALL_SCRIPT_PATH}"
sudo chmod +x "${INSTALL_SCRIPT_PATH}"

sudo chown -R caddy:caddy /srv/repo/public 2>/dev/null || sudo chown -R www-data:www-data /srv/repo/public 2>/dev/null || true
sudo chmod -R 755 /srv/repo/public

echo "--- [Step 6/7] Repository files configured..."
echo "--- [Step 7/7] Finalizing setup and validation..."

# Validate the deployment
echo "🔍 Validating repository setup..."

# Check that required files were created
if [ ! -f "${INSTALL_SCRIPT_PATH}" ]; then
    echo "❌ ERROR: install.sh was not created at ${INSTALL_SCRIPT_PATH}" >&2
    exit 1
fi

if [ ! -f "${WEB_ROOT_DIR}/${GPG_PUBLIC_KEY_FILE}" ]; then
    echo "❌ ERROR: GPG public key was not created" >&2
    exit 1
fi

if [ ! -d "${WEB_ROOT_DIR}/aptly" ] || [ -z "$(ls -A "${WEB_ROOT_DIR}/aptly")" ]; then
    echo "❌ ERROR: aptly directory is empty or missing" >&2
    exit 1
fi

# Verify install.sh has correct domain
if ! grep -q "REPO_HOST=\"${REPO_DOMAIN}\"" "${INSTALL_SCRIPT_PATH}"; then
    echo "❌ ERROR: install.sh has incorrect domain. Expected: ${REPO_DOMAIN}" >&2
    grep "REPO_HOST=" "${INSTALL_SCRIPT_PATH}" >&2 || true
    exit 1
fi

# Check file permissions
if [ ! -x "${INSTALL_SCRIPT_PATH}" ]; then
    echo "❌ ERROR: install.sh is not executable" >&2
    exit 1
fi

echo "✅ All validation checks passed!"
echo
echo "========================================================================"
echo "✅ Repository setup complete!"
echo "📦 Repository type: ${RELEASE_TYPE}"
echo "📍 Repository URL: https://${REPO_DOMAIN}/${REPO_URL_PATH}/"
echo "🔧 Install command:"
echo "   curl -sSL https://${REPO_DOMAIN}/${REPO_URL_PATH}/install.sh | sudo bash"
echo "========================================================================"