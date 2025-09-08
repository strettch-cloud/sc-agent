#!/bin/bash
# SC-Agent Repository Setup Script
# Builds packages and sets up APT repository on the repository server

set -euo pipefail

# Configuration
ORG_NAME="strettch"
GPG_EMAIL="engineering@strettch.com"
PACKAGE_NAME="sc-console-agent"
REPO_DOMAIN="repo.cloud.strettch.dev"
DISTRIBUTIONS="bionic focal jammy noble oracular"
WEB_ROOT_DIR="/srv/repo/public/console"

# Get version from git tags
get_version() {
    if git describe --exact-match --tags HEAD 2>/dev/null; then
        git describe --exact-match --tags HEAD | sed 's/^v//'
    elif git describe --tags --abbrev=0 2>/dev/null; then
        git describe --tags --abbrev=0 | sed 's/^v//'
    else
        echo "0.0.1-$(git rev-parse --short HEAD)"
    fi
}

PACKAGE_VERSION=$(get_version)
echo "Building version: ${PACKAGE_VERSION}"

# Cleanup on exit
cleanup() {
    rm -rf "${STAGING_DIR:-/tmp/${PACKAGE_NAME}-build}"
}
trap cleanup EXIT

# Check required tools
for tool in fpm aptly gpg; do
    if ! command -v $tool &> /dev/null; then
        echo "Error: $tool is not installed" >&2
        exit 1
    fi
done

# Asset paths
SERVICE_FILE="packaging/systemd/${PACKAGE_NAME}.service"
UPDATER_SERVICE="packaging/systemd/${PACKAGE_NAME}-update-scheduler.service"
UPDATER_TIMER="packaging/systemd/${PACKAGE_NAME}-update-scheduler.timer"
UPDATER_SCRIPT="packaging/scripts/${PACKAGE_NAME}-updater.sh"
POSTINSTALL_SCRIPT="packaging/scripts/post-install.sh"
PREREMOVE_SCRIPT="packaging/scripts/pre-remove.sh"

echo "--- [Step 1/7] Cleaning up previous packages..."
rm -f ${PACKAGE_NAME}_*.deb

echo "--- [Step 2/7] Building Go binary..."
GOOS=linux GOARCH=amd64 make build VERSION="${PACKAGE_VERSION}"

# Verify required files exist
echo "--- [Step 3/7] Verifying required files..."
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
        exit 1
    fi
done

echo "--- [Step 4/7] Preparing package structure..."
STAGING_DIR="/tmp/${PACKAGE_NAME}-build"
rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}/usr/bin" "${STAGING_DIR}/etc/systemd/system"

# Copy binaries to /usr/bin (standard location for packaged software)
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"

cp "${UPDATER_SCRIPT}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"

# Copy systemd service files
cp "${SERVICE_FILE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_SERVICE}" "${STAGING_DIR}/etc/systemd/system/"
cp "${UPDATER_TIMER}" "${STAGING_DIR}/etc/systemd/system/"

echo "--- [Step 5/7] Building .deb package..."
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

echo "--- [Step 6/7] Adding package to APT repository..."
if ! sudo aptly repo show sc-console-agent-repo > /dev/null 2>&1; then
    sudo aptly repo create -distribution="focal" -component="main" sc-console-agent-repo
fi

sudo aptly repo add sc-console-agent-repo ${PACKAGE_NAME}_${PACKAGE_VERSION#v}_amd64.deb
SNAPSHOT_NAME="${PACKAGE_NAME}-${PACKAGE_VERSION}"
sudo aptly snapshot create "${SNAPSHOT_NAME}" from repo sc-console-agent-repo

# Configure GPG
export GPG_TTY=$(tty)
unset DISPLAY

GPG_FINGERPRINT=$(gpg --list-secret-keys --with-colons "${GPG_EMAIL}" | awk -F: '/^sec/{print $5; exit}')
if [ -z "${GPG_FINGERPRINT}" ]; then
    echo "Error: Could not find GPG key for ${GPG_EMAIL}" >&2
    exit 1
fi

# Publish for all distributions
for dist in ${DISTRIBUTIONS}; do
    echo "Publishing for ${dist}..."
    sudo aptly publish drop "${dist}" 2>/dev/null || true
    sudo aptly publish snapshot -gpg-key="${GPG_FINGERPRINT}" -distribution="${dist}" -batch "${SNAPSHOT_NAME}"
done

echo "--- [Step 7/7] Setting up web directory..."
sudo mkdir -p "${WEB_ROOT_DIR}/aptly"
sudo rsync -a --delete ~/.aptly/public/ "${WEB_ROOT_DIR}/aptly/"

# Export GPG public key
gpg --armor --export "${GPG_EMAIL}" > "${PACKAGE_NAME}-repo.gpg"
sudo mv "${PACKAGE_NAME}-repo.gpg" "${WEB_ROOT_DIR}/"

# Generate install script
TMP_INSTALL_SCRIPT="/tmp/install.sh"
cat << 'INSTALL_EOF' > "${TMP_INSTALL_SCRIPT}"
#!/bin/sh
set -e

REPO_HOST="repo.cloud.strettch.dev"
REPO_PATH="console"
PACKAGE_NAME="sc-console-agent"
GPG_KEY_FILENAME="sc-console-agent-repo.gpg"

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

# Fallback for unsupported distributions
SUPPORTED_DISTS="bionic focal jammy noble oracular"
if ! echo "$SUPPORTED_DISTS" | grep -q "$DISTRIBUTION"; then
    echo "Warning: $DISTRIBUTION not supported, using noble"
    DISTRIBUTION="noble"
fi

echo "Installing ${PACKAGE_NAME} for ${DISTRIBUTION}..."

# Clean up old configurations
rm -f /etc/apt/sources.list.d/sc-console-agent.list 2>/dev/null || true
rm -f /usr/share/keyrings/sc-console-agent-keyring.gpg 2>/dev/null || true

# Install prerequisites
apt-get update
apt-get install -y apt-transport-https ca-certificates curl gnupg

# Add repository
KEYRING_FILE="/usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg"
curl -fsSL "https://${REPO_HOST}/${REPO_PATH}/${GPG_KEY_FILENAME}" | gpg --dearmor -o "${KEYRING_FILE}"

echo "deb [signed-by=${KEYRING_FILE}] https://${REPO_HOST}/${REPO_PATH}/aptly ${DISTRIBUTION} main" > /etc/apt/sources.list.d/${PACKAGE_NAME}.list

# Install package
apt-get update
apt-get install -y ${PACKAGE_NAME}

echo "✅ ${PACKAGE_NAME} installed successfully!"
exit 0
INSTALL_EOF

sudo mv "${TMP_INSTALL_SCRIPT}" "${WEB_ROOT_DIR}/install.sh"
sudo chmod +x "${WEB_ROOT_DIR}/install.sh"

# Set permissions
sudo chown -R caddy:caddy /srv/repo/public 2>/dev/null || \
sudo chown -R www-data:www-data /srv/repo/public 2>/dev/null || true
sudo chmod -R 755 /srv/repo/public

echo ""
echo "========================================================================"
echo "✅ Repository setup complete!"
echo ""
echo "Package version: ${PACKAGE_VERSION}"
echo "Installation command:"
echo "  curl -sSL https://${REPO_DOMAIN}/console/install.sh | sudo bash"
echo "========================================================================"