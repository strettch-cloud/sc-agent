#!/bin/bash
# Repository Setup Script
# Purpose: Build packages and set up APT repository on the repository server
# Usage: ./setup_repo.sh (run on repository server)
# Location: This script runs on the repository server at /root/sc-agent/

set -e

# --- Configuration ---
ORG_NAME="strettch"
GPG_EMAIL="engineering@strettch.com"
PACKAGE_NAME="sc-agent"
PACKAGE_VERSION=$(git describe --tags --always --dirty)
REPO_DOMAIN="repo.cloud.strettch.dev"  # Production repository domain
DISTRIBUTIONS="focal jammy noble"  # Ubuntu versions to support
WEB_ROOT_DIR="/srv/repo/public/console"
# ---

# --- Asset Paths ---
SERVICE_FILE="packaging/systemd/${PACKAGE_NAME}.service"
UPDATER_SERVICE="packaging/systemd/${PACKAGE_NAME}-updater.service"
UPDATER_TIMER="packaging/systemd/${PACKAGE_NAME}-update-scheduler.timer"
UPDATER_SCRIPT="packaging/scripts/${PACKAGE_NAME}-updater.sh"
POSTINSTALL_SCRIPT="packaging/scripts/post-install.sh"
PREREMOVE_SCRIPT="packaging/scripts/pre-remove.sh"
CONFIG_FILE="config.example.yaml"
# ---

# --- Pre-flight Checks ---
echo "Checking required files..."
required_files=(
    "$SERVICE_FILE"
    "$UPDATER_SERVICE"
    "$UPDATER_TIMER"
    "$UPDATER_SCRIPT"
    "$POSTINSTALL_SCRIPT"
    "$PREREMOVE_SCRIPT"
    "$CONFIG_FILE"
    "build/${PACKAGE_NAME}"
)

for file in "${required_files[@]}"; do
    if [ ! -f "$file" ]; then
        echo "Error: Required file not found: ${file}" >&2
        echo "Run 'make build' first if binary is missing" >&2
        exit 1
    fi
done

# Check for required tools (these should be installed on the repository server)
for tool in fpm aptly gpg; do
    if ! command -v $tool &> /dev/null; then
        echo "Error: $tool is not installed" >&2
        exit 1
    fi
done
# ---

echo "--- [Step 1/7] Cleaning up previous repository publications..."
# Clean up old Aptly publications and snapshots
for dist in ${DISTRIBUTIONS}; do
    sudo aptly publish drop ${dist} 2>/dev/null || true
done
sudo aptly repo remove sc-agent-repo ${PACKAGE_NAME} 2>/dev/null || echo "No package to remove from repo. Continuing."
sudo aptly db cleanup
rm -f ${PACKAGE_NAME}_*.deb

echo "--- [Step 2/7] Building Go binary via Makefile..."
GOOS=linux GOARCH=amd64 make build

echo "--- [Step 3/7] Preparing packaging assets..."
# Prepare staging directory structure
STAGING_DIR="/tmp/${PACKAGE_NAME}-build"
rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}/usr/local/bin"
mkdir -p "${STAGING_DIR}/etc/${PACKAGE_NAME}" 
mkdir -p "${STAGING_DIR}/etc/systemd/system"

# Copy files to staging
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/local/bin/"
chmod +x "${STAGING_DIR}/usr/local/bin/${PACKAGE_NAME}"

cp "${UPDATER_SCRIPT}" "${STAGING_DIR}/usr/local/bin/"
chmod +x "${STAGING_DIR}/usr/local/bin/${PACKAGE_NAME}-updater.sh"

cp "${CONFIG_FILE}" "${STAGING_DIR}/etc/${PACKAGE_NAME}/config.yaml"
cp "${SERVICE_FILE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_SERVICE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_TIMER}" "${STAGING_DIR}/etc/systemd/system/"

# Build .deb package with FPM
fpm -s dir -t deb \
    -n ${PACKAGE_NAME} \
    -v ${PACKAGE_VERSION#v} \
    -C "${STAGING_DIR}/" \
    --description "SC Agent for SSH access management by ${ORG_NAME}" \
    --maintainer "${GPG_EMAIL}" \
    --url "https://github.com/strettch/sc-agent" \
    --depends "libc6" --depends "dmidecode" --depends "jq" --depends "curl" \
    --after-install "${POSTINSTALL_SCRIPT}" \
    --before-remove "${PREREMOVE_SCRIPT}"

echo "--- [Step 4/7] Building the .deb package..."
# Create Aptly repository if it doesn't exist
if ! sudo aptly repo show sc-agent-repo > /dev/null 2>&1; then
    sudo aptly repo create -distribution="focal" -component="main" sc-agent-repo
fi

# Add package to repository
sudo aptly repo add sc-agent-repo ${PACKAGE_NAME}_${PACKAGE_VERSION#v}_amd64.deb

# Create snapshot
SNAPSHOT_NAME="${PACKAGE_NAME}-${PACKAGE_VERSION}"
sudo aptly snapshot create "${SNAPSHOT_NAME}" from repo sc-agent-repo

# Configure GPG environment for batch mode
export GPG_TTY=$(tty)
unset DISPLAY

# Check if GPG passphrase file exists
if [ -f "/root/gpg-passphrase.txt" ]; then
    echo "Using GPG passphrase file for signing..."
    PASSPHRASE_OPTION="-passphrase-file=/root/gpg-passphrase.txt"
else
    echo "Warning: No GPG passphrase file found at /root/gpg-passphrase.txt"
    echo "Attempting to use key without passphrase..."
    PASSPHRASE_OPTION=""
fi

for dist in ${DISTRIBUTIONS}; do
    echo "Publishing for ${dist}..."
    if [ -n "$PASSPHRASE_OPTION" ]; then
        sudo aptly publish snapshot -gpg-key="${GPG_EMAIL}" -distribution="${dist}" \
            -batch $PASSPHRASE_OPTION "${SNAPSHOT_NAME}"
    else
        sudo aptly publish snapshot -gpg-key="${GPG_EMAIL}" -distribution="${dist}" \
            -batch "${SNAPSHOT_NAME}"
    fi
done

echo "--- [Step 5/7] Creating and Publishing the new version..."
# Copy repository files to web root
sudo mkdir -p "${WEB_ROOT_DIR}/aptly"
sudo rsync -a --delete ~/.aptly/public/ "${WEB_ROOT_DIR}/aptly/"

# Export GPG public key
GPG_PUBLIC_KEY_FILE="${PACKAGE_NAME}-repo.gpg"
gpg --armor --export "${GPG_EMAIL}" > "${GPG_PUBLIC_KEY_FILE}"
sudo mv "${GPG_PUBLIC_KEY_FILE}" "${WEB_ROOT_DIR}/"

# Generate the install.sh script dynamically (like sc-metrics-agent does)
INSTALL_SCRIPT_PATH="${WEB_ROOT_DIR}/install.sh"
TMP_INSTALL_SCRIPT=$(mktemp)
cat << 'INSTALL_EOF' > "${TMP_INSTALL_SCRIPT}"
#!/bin/sh
set -e
# --- Configuration ---
REPO_HOST="repo.cloud.strettch.dev"
REPO_PATH="console"  # Subdirectory for sc-agent
PACKAGE_NAME="sc-agent"
GPG_KEY_FILENAME="sc-agent-repo.gpg"
CONFIG_FILE="/etc/sc-agent/config.yaml"
# ---
if [ "$(id -u)" -ne "0" ]; then
    echo "This script must be run as root. Please use 'sudo'." >&2
    exit 1
fi
if [ -f /etc/os-release ]; then
    DISTRIBUTION=$(. /etc/os-release && echo "$VERSION_CODENAME")
else
    echo "Error: Unable to detect OS distribution codename." >&2
    exit 1
fi
echo "--- Installing ${PACKAGE_NAME} for ${DISTRIBUTION} ---"

log_and_run() {
    echo "$1"
    if ! eval "$2"; then
        echo "Error: Failed to $3. Please check the output above for details." >&2
        exit 1
    fi
}

log_and_run "Updating package lists..." "apt-get update > /dev/null" "update package lists"
log_and_run "Installing prerequisite packages..." "apt-get install -y apt-transport-https ca-certificates curl gnupg > /dev/null" "install prerequisite packages"

KEYRING_FILE="/usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg"
log_and_run "Downloading and installing GPG key..." "curl -fsSL \"https://${REPO_HOST}/${REPO_PATH}/${GPG_KEY_FILENAME}\" | gpg --dearmor | sudo tee \"${KEYRING_FILE}\" >/dev/null" "download and install GPG key"

SOURCES_FILE="/etc/apt/sources.list.d/${PACKAGE_NAME}.list"
log_and_run "Adding ${PACKAGE_NAME} repository..." "echo \"deb [signed-by=${KEYRING_FILE}] https://${REPO_HOST}/${REPO_PATH}/aptly ${DISTRIBUTION} main\" | sudo tee \"${SOURCES_FILE}\" >/dev/null" "add ${PACKAGE_NAME} repository"

log_and_run "Updating package list for ${PACKAGE_NAME}..." "apt-get update -o Dir::Etc::SourceList='${SOURCES_FILE}' -o Dir::Etc::SourceParts='-' -o APT::Get::List-Cleanup='0' > /dev/null" "update package list for ${PACKAGE_NAME}"

log_and_run "Installing ${PACKAGE_NAME}..." "apt-get install -y \"${PACKAGE_NAME}\"" "install ${PACKAGE_NAME}"
echo
echo "✅ ${PACKAGE_NAME} was installed successfully!"
exit 0
INSTALL_EOF

sudo mv "${TMP_INSTALL_SCRIPT}" "${INSTALL_SCRIPT_PATH}"
sudo chmod +x "${INSTALL_SCRIPT_PATH}"

# Set permissions
sudo chown -R caddy:caddy /srv/repo/public 2>/dev/null || \
sudo chown -R www-data:www-data /srv/repo/public 2>/dev/null || true
sudo chmod -R 755 /srv/repo/public

echo "--- [Step 6/7] Repository files configured..."
echo "--- [Step 7/7] Finalizing setup..."
# Note: Caddy configuration is now managed separately for both agents
# The main Caddyfile handles routing to /console/ and /metrics/ subdirectories

echo
echo "========================================================================"
echo "✅ Repository setup complete!"
echo "To install the agent on a client machine, run:"
echo "curl -sSL https://${REPO_DOMAIN}/console/install.sh | sudo bash"
echo "======================================================================="