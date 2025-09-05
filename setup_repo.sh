#!/bin/bash
# Repository Setup Script
# Purpose: Build packages and set up APT repository on the repository server
# Usage: ./setup_repo.sh (run on repository server)
# Location: This script runs on the repository server at /root/sc-console-agent/

set -e

# --- Configuration ---
ORG_NAME="strettch"
GPG_EMAIL="engineering@strettch.com"
PACKAGE_NAME="sc-console-agent"
PACKAGE_VERSION=$(git describe --tags --always --dirty)
REPO_DOMAIN="repo.cloud.strettch.dev"  # Production repository domain
DISTRIBUTIONS="focal jammy noble"  # Ubuntu versions to support
WEB_ROOT_DIR="/srv/repo/public/console"
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
for dist in ${DISTRIBUTIONS}; do
    sudo aptly publish drop ${dist} 2>/dev/null || true
done
sudo aptly repo remove sc-console-agent-repo ${PACKAGE_NAME} 2>/dev/null || echo "No package to remove. Continuing."
sudo aptly db cleanup
rm -f ${PACKAGE_NAME}_*.deb

echo "--- [Step 2/7] Building Go binary via Makefile..."
GOOS=linux GOARCH=amd64 make build

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
mkdir -p "${STAGING_DIR}/usr/local/bin" "${STAGING_DIR}/etc/systemd/system"

# Copy files
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/local/bin/"
chmod +x "${STAGING_DIR}/usr/local/bin/${PACKAGE_NAME}"

cp "${UPDATER_SCRIPT}" "${STAGING_DIR}/usr/local/bin/"
chmod +x "${STAGING_DIR}/usr/local/bin/${PACKAGE_NAME}-updater.sh"

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
    --url "https://github.com/strettch/sc-console-agent" \
    --depends "libc6" --depends "dmidecode" --depends "jq" --depends "curl" \
    --after-install "${POSTINSTALL_SCRIPT}" \
    --before-remove "${PREREMOVE_SCRIPT}"

echo "--- [Step 4/7] Building the .deb package..."
if ! sudo aptly repo show sc-console-agent-repo > /dev/null 2>&1; then
    sudo aptly repo create -distribution="focal" -component="main" sc-console-agent-repo
fi

sudo aptly repo add sc-console-agent-repo ${PACKAGE_NAME}_${PACKAGE_VERSION#v}_amd64.deb
SNAPSHOT_NAME="${PACKAGE_NAME}-${PACKAGE_VERSION}"
sudo aptly snapshot create "${SNAPSHOT_NAME}" from repo sc-console-agent-repo

# --- Configure GPG environment ---
export GPG_TTY=$(tty)
unset DISPLAY

# Get the fingerprint for the GPG key
GPG_FINGERPRINT=$(gpg --list-secret-keys --with-colons "${GPG_EMAIL}" | awk -F: '/^sec/{print $5; exit}')
if [ -z "${GPG_FINGERPRINT}" ]; then
    echo "Error: Could not find GPG key for ${GPG_EMAIL}" >&2
    exit 1
fi

# Publish snapshots with correct fingerprint
for dist in ${DISTRIBUTIONS}; do
    echo "Publishing for ${dist}..."
    sudo aptly publish snapshot -gpg-key="${GPG_FINGERPRINT}" -distribution="${dist}" -batch "${SNAPSHOT_NAME}"
done

echo "--- [Step 5/7] Creating and Publishing the new version..."
sudo mkdir -p "${WEB_ROOT_DIR}/aptly"
sudo rsync -a --delete ~/.aptly/public/ "${WEB_ROOT_DIR}/aptly/"

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
REPO_HOST="repo.cloud.strettch.dev"
REPO_PATH="console"
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

sudo mv "${TMP_INSTALL_SCRIPT}" "${INSTALL_SCRIPT_PATH}"
sudo chmod +x "${INSTALL_SCRIPT_PATH}"

sudo chown -R caddy:caddy /srv/repo/public 2>/dev/null || sudo chown -R www-data:www-data /srv/repo/public 2>/dev/null || true
sudo chmod -R 755 /srv/repo/public

echo "--- [Step 6/7] Repository files configured..."
echo "--- [Step 7/7] Finalizing setup..."
echo
echo "========================================================================"
echo "✅ Repository setup complete!"
echo "To install the agent on a client machine, run:"
echo "curl -sSL https://${REPO_DOMAIN}/console/install.sh | sudo bash"
echo "======================================================================="
