#!/bin/bash
#
# Repository Setup Script
#
# Purpose: Builds the Go application, creates .deb packages, and manages the
#          APT repository using aptly on the repository server.
#
# Usage:   RELEASE_TYPE=<stable|beta> [PACKAGE_VERSION=<version>] ./setup_repo.sh
# Example: RELEASE_TYPE=beta ./setup_repo.sh
#          RELEASE_TYPE=stable PACKAGE_VERSION=1.2.3 ./setup_repo.sh
#
# Location: This script is intended to run on the repository server, typically
#           at /root/sc-console-agent/
#

# --- Script Configuration and Initialization ---

# Exit immediately on error, treat unset variables as error, fail on pipe errors
set -euo pipefail

# Enhanced error handling with line numbers and cleanup
trap 'handle_error $? $LINENO' ERR
trap 'cleanup' EXIT

# --- Error Handling Functions ---
handle_error() {
    local exit_code=$1
    local line_number=$2
    echo "❌ Error occurred in setup_repo.sh at line ${line_number}. Exit code: ${exit_code}" >&2
    cleanup
    exit "${exit_code}"
}

cleanup() {
    # Clean up temporary files on exit
    rm -f "${PACKAGE_NAME}"_*.deb 2>/dev/null || true
    [ -n "${STAGING_DIR:-}" ] && rm -rf "${STAGING_DIR}" 2>/dev/null || true
}

# --- Read-only Variables and Constants ---
readonly ORG_NAME="strettch"
readonly GPG_EMAIL="engineering@strettch.com"
readonly PACKAGE_NAME="sc-console-agent"
readonly REPO_DOMAIN="repo.cloud.strettch.com"
readonly DISTRIBUTIONS="bionic focal jammy noble" # Supported Ubuntu versions
readonly KEEP_VERSIONS=5                          # Number of package versions to retain
readonly KEEP_SNAPSHOTS=10                        # Number of snapshots to retain

# --- Logging Functions ---
log_info() {
    echo "ℹ️  $1"
}

log_success() {
    echo "✅ $1"
}

log_error() {
    echo "❌ $1" >&2
}

log_step() {
    local step_num=$1
    local total_steps=$2
    local message=$3
    echo ""
    echo "--- [Step ${step_num}/${total_steps}] ${message}..."
}

# --- Core Functions ---

# Sets up dynamic variables based on the release type
setup_variables() {
    RELEASE_TYPE="${RELEASE_TYPE:-stable}"
    log_info "Release type determined as: ${RELEASE_TYPE}"
    
    # Set architectures based on release type
    ARCHES="amd64 arm64"
    log_info "Build architectures: ${ARCHES}"
    
    # Set paths based on release type
    if [[ "$RELEASE_TYPE" == "beta" ]]; then
        WEB_ROOT_DIR="/srv/repo/public/console/beta"
        REPO_URL_PATH="console/beta"
        REPO_NAME="${PACKAGE_NAME}-beta-repo"
        APTLY_NAMESPACE="console-beta"
    else
        WEB_ROOT_DIR="/srv/repo/public/console"
        REPO_URL_PATH="console"
        REPO_NAME="${PACKAGE_NAME}-stable-repo"
        APTLY_NAMESPACE="console"
    fi
    
    log_info "Configuration:"
    echo "   - Web Root: ${WEB_ROOT_DIR}"
    echo "   - Repo URL Path: ${REPO_URL_PATH}"
    echo "   - Aptly Repo Name: ${REPO_NAME}"
    echo "   - Aptly Namespace: ${APTLY_NAMESPACE}"
    
    # Asset paths
    SERVICE_FILE="packaging/systemd/${PACKAGE_NAME}.service"
    UPDATER_SERVICE="packaging/systemd/${PACKAGE_NAME}-update-scheduler.service"
    UPDATER_TIMER="packaging/systemd/${PACKAGE_NAME}-update-scheduler.timer"
    UPDATER_SCRIPT="packaging/scripts/${PACKAGE_NAME}-updater.sh"
    CONFIG_DOWNLOAD_SCRIPT="packaging/scripts/download-config.sh"
    POSTINSTALL_SCRIPT="packaging/scripts/post-install.sh"
    PREREMOVE_SCRIPT="packaging/scripts/pre-remove.sh"
}

# Determines the correct package version based on git tags
get_package_version() {
    if [[ -n "${PACKAGE_VERSION:-}" ]]; then
        log_info "Using provided package version: ${PACKAGE_VERSION}"
        return
    fi
    
    log_info "Auto-detecting package version from git tags..."
    local latest_tag
    
    if [[ "$RELEASE_TYPE" == "stable" ]]; then
        # For stable, find the latest tag matching X.Y.Z format
        latest_tag=$(git tag -l --sort=-version:refname | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")
    else
        # For beta, use the most recent tag
        latest_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    fi
    
    if [[ -n "$latest_tag" ]]; then
        PACKAGE_VERSION="${latest_tag#v}" # Remove 'v' prefix if present
        log_info "Found latest tag: ${PACKAGE_VERSION}"
    else
        PACKAGE_VERSION="0.1.0"
        log_info "No suitable tags found. Using default: ${PACKAGE_VERSION}"
    fi
}

# Checks for required command-line tools
check_dependencies() {
    local missing_tools=()
    
    for tool in fpm aptly gpg go make git jq; do
        if ! command -v "$tool" &>/dev/null; then
            missing_tools+=("$tool")
        fi
    done
    
    if [ ${#missing_tools[@]} -gt 0 ]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        echo "Install with: apt-get install ${missing_tools[*]}"
        exit 1
    fi
    
    log_success "All required tools are present"
}

# Cleans up old build artifacts and broken aptly states
cleanup_previous_builds() {
    log_info "Cleaning up previous build artifacts..."
    rm -f "${PACKAGE_NAME}"_*.deb
    
    log_info "Checking for broken aptly publications..."
    for dist in ${DISTRIBUTIONS}; do
        if sudo aptly publish show "${dist}" "${APTLY_NAMESPACE}" >/dev/null 2>&1; then
            if ! sudo aptly publish show "${dist}" "${APTLY_NAMESPACE}" 2>/dev/null | grep -q "Snapshot:"; then
                log_info "Removing broken publication for ${dist}"
                sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" --force-drop 2>/dev/null || true
            fi
        fi
    done
    
    log_success "Cleanup complete"
}

# Verifies all required packaging files exist
verify_required_files() {
    local required_files=(
        "$SERVICE_FILE"
        "$UPDATER_SERVICE"
        "$UPDATER_TIMER"
        "$UPDATER_SCRIPT"
        "$CONFIG_DOWNLOAD_SCRIPT"
        "$POSTINSTALL_SCRIPT"
        "$PREREMOVE_SCRIPT"
    )
    
    for file in "${required_files[@]}"; do
        if [[ ! -f "$file" ]]; then
            log_error "Required packaging file not found: ${file}"
            return 1
        fi
    done
    
    log_success "All packaging files verified"
    return 0
}

# Builds Go binaries for all target architectures
build_go_binaries() {
    for arch in ${ARCHES}; do
        log_info "Building Go binary for ${arch}..."
        GOOS=linux GOARCH="${arch}" make build VERSION="${PACKAGE_VERSION}"
        
        if [[ ! -f "build/${PACKAGE_NAME}" ]]; then
            log_error "Go binary not found after build for ${arch}"
            exit 1
        fi
        
        # Rename to architecture-specific name
        mv "build/${PACKAGE_NAME}" "build/${PACKAGE_NAME}-${arch}"
    done
    
    log_success "All binaries built successfully"
}

# Creates Debian packages for all architectures
build_deb_packages() {
    for arch in ${ARCHES}; do
        log_info "Creating Debian package for ${arch}..."
        
        STAGING_DIR="/tmp/${PACKAGE_NAME}-build-${arch}"
        rm -rf "${STAGING_DIR}"
        mkdir -p "${STAGING_DIR}/usr/bin" "${STAGING_DIR}/etc/systemd/system" "${STAGING_DIR}/usr/lib/${PACKAGE_NAME}"

        # Copy binary
        cp "build/${PACKAGE_NAME}-${arch}" "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"
        chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"

        # Copy updater script
        cp "${UPDATER_SCRIPT}" "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"
        chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"

        # Copy config download script
        cp "${CONFIG_DOWNLOAD_SCRIPT}" "${STAGING_DIR}/usr/lib/${PACKAGE_NAME}/download-config.sh"
        chmod +x "${STAGING_DIR}/usr/lib/${PACKAGE_NAME}/download-config.sh"
        
        # Copy systemd files
        cp "${SERVICE_FILE}" "${STAGING_DIR}/etc/systemd/system/"
        cp "${UPDATER_SERVICE}" "${STAGING_DIR}/etc/systemd/system/"
        cp "${UPDATER_TIMER}" "${STAGING_DIR}/etc/systemd/system/"
        
        # Build package with FPM
        fpm -s dir -t deb \
            -n "${PACKAGE_NAME}" \
            -v "${PACKAGE_VERSION}" \
            -a "${arch}" \
            -C "${STAGING_DIR}/" \
            --description "SC Agent for SSH access management by ${ORG_NAME}" \
            --maintainer "${GPG_EMAIL}" \
            --url "https://github.com/strettch/sc-console-agent" \
            --depends "libc6" --depends "dmidecode" --depends "jq" --depends "curl" \
            --after-install "${POSTINSTALL_SCRIPT}" \
            --before-remove "${PREREMOVE_SCRIPT}" \
            --log error
        
        if [[ ! -f "${PACKAGE_NAME}_${PACKAGE_VERSION}_${arch}.deb" ]]; then
            log_error "Package creation failed for ${arch}"
            exit 1
        fi
        
        rm -rf "${STAGING_DIR}"
    done
    
    log_success "All packages created successfully"
}

# Manages the aptly repository
manage_aptly_repository() {
    log_info "Managing APT repository with aptly..."
    
    # Create repository if it doesn't exist
    if ! sudo aptly repo show "$REPO_NAME" >/dev/null 2>&1; then
        log_info "Creating new aptly repository: ${REPO_NAME}"
        sudo aptly repo create -distribution="focal" -component="main" "$REPO_NAME"
    fi
    
    # Clean up old package versions
    clean_old_package_versions
    
    # Add new packages
    log_info "Adding new packages to repository..."
    sudo aptly repo add "$REPO_NAME" "${PACKAGE_NAME}"_*.deb
    
    # Create snapshot
    create_repository_snapshot
    
    # Clean up old snapshots
    clean_old_snapshots
    
    log_success "Aptly repository updated"
}

# Removes old package versions from repository
clean_old_package_versions() {
    log_info "Cleaning up old package versions (keeping last ${KEEP_VERSIONS})..."
    
    local current_packages
    current_packages=$(sudo aptly repo show -with-packages "$REPO_NAME" 2>/dev/null | \
                      grep "^  ${PACKAGE_NAME}_" | sed 's/^  //' || echo "")
    
    if [[ -z "$current_packages" ]]; then
        log_info "No existing packages to clean"
        return
    fi
    
    # Get unique versions (removing architecture suffixes)
    local unique_versions
    unique_versions=$(echo "$current_packages" | \
                     sed -E 's/_[^_]+$//' | sort -Vu)
    
    local version_count
    version_count=$(echo "$unique_versions" | grep -c . || echo 0)
    
    if [ "$version_count" -ge "$KEEP_VERSIONS" ]; then
        local versions_to_remove=$((version_count - KEEP_VERSIONS + 1))
        log_info "Removing ${versions_to_remove} old version(s)..."
        
        echo "$unique_versions" | head -n "$versions_to_remove" | while read -r old_version; do
            log_info "Removing version: ${old_version}"
            # Remove all architectures for this version
            sudo aptly repo remove "$REPO_NAME" "${PACKAGE_NAME} (= ${old_version#*_})" || true
        done
    else
        log_info "No old versions to remove"
    fi
}

# Creates a new snapshot from the repository
create_repository_snapshot() {
    local snapshot_name="${REPO_NAME}-${PACKAGE_VERSION}"
    log_info "Creating snapshot: ${snapshot_name}"
    
    # Remove existing snapshot if present
    if sudo aptly snapshot show "${snapshot_name}" >/dev/null 2>&1; then
        log_info "Removing existing snapshot with same name..."
        # First drop any publications using this snapshot
        for dist in ${DISTRIBUTIONS}; do
            sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" 2>/dev/null || true
        done
        sudo aptly snapshot drop "${snapshot_name}" -force
    fi
    
    sudo aptly snapshot create "${snapshot_name}" from repo "$REPO_NAME"
    SNAPSHOT_NAME="${snapshot_name}"
}

# Removes old snapshots
clean_old_snapshots() {
    log_info "Cleaning up old snapshots (keeping last ${KEEP_SNAPSHOTS})..."
    
    local snapshot_pattern
    if [ "$RELEASE_TYPE" = "beta" ]; then
        snapshot_pattern="^${REPO_NAME}-.*-beta"
    else
        # For stable, exclude beta snapshots
        snapshot_pattern="^${REPO_NAME}-[^-]*-[^-]*-[^-b]*$"
    fi
    
    local old_snapshots
    old_snapshots=$(sudo aptly snapshot list | grep "${snapshot_pattern}" | \
                   awk '{print $1}' | tr -d '[]' | sort -V | head -n -${KEEP_SNAPSHOTS} || echo "")
    
    if [[ -n "$old_snapshots" ]]; then
        echo "$old_snapshots" | while read -r snapshot; do
            log_info "Removing old snapshot: ${snapshot}"
            sudo aptly snapshot drop "${snapshot}" -force 2>/dev/null || true
        done
    else
        log_info "No old snapshots to remove"
    fi
}

# Publishes the repository snapshots
publish_repository() {
    log_info "Publishing repository..."
    
    # Configure GPG environment
    export GPG_TTY=$(tty)
    unset DISPLAY
    
    # Get GPG fingerprint
    local gpg_fingerprint
    gpg_fingerprint=$(gpg --list-secret-keys --with-colons "${GPG_EMAIL}" | \
                     awk -F: '/^sec/{print $5; exit}')
    
    if [[ -z "$gpg_fingerprint" ]]; then
        log_error "Could not find GPG key for ${GPG_EMAIL}"
        exit 1
    fi
    
    # Publish for each distribution
    for dist in ${DISTRIBUTIONS}; do
        log_info "Publishing for ${dist} to namespace ${APTLY_NAMESPACE}..."
        # Drop existing publication
        sudo aptly publish drop "${dist}" "${APTLY_NAMESPACE}" 2>/dev/null || true
        # Publish new snapshot
        sudo aptly publish snapshot \
            -gpg-key="${gpg_fingerprint}" \
            -distribution="${dist}" \
            -batch \
            "${SNAPSHOT_NAME}" "${APTLY_NAMESPACE}"
    done
    
    log_success "Repository published"
}

# Deploys repository assets to web root
deploy_repository_assets() {
    log_info "Deploying repository assets to web root..."
    
    # Ensure web root exists
    sudo mkdir -p "${WEB_ROOT_DIR}"
    
    # Sync aptly public directory
    if [[ -d ~/.aptly/public/${APTLY_NAMESPACE} ]]; then
        sudo rsync -a --delete ~/.aptly/public/"${APTLY_NAMESPACE}"/ "${WEB_ROOT_DIR}/aptly/"
        log_success "Repository files synced to ${WEB_ROOT_DIR}"
    else
        log_error "Aptly namespace ${APTLY_NAMESPACE} not found"
        exit 1
    fi
    
    # Export GPG public key
    local gpg_key_file="${PACKAGE_NAME}-repo.gpg"
    gpg --armor --export "${GPG_EMAIL}" | sudo tee "${WEB_ROOT_DIR}/${gpg_key_file}" >/dev/null
    
    # Generate install script
    generate_install_script "${gpg_key_file}"
    
    # Set proper permissions
    if id "caddy" &>/dev/null; then
        sudo chown -R caddy:caddy /srv/repo/public
    elif id "www-data" &>/dev/null; then
        sudo chown -R www-data:www-data /srv/repo/public
    fi
    sudo chmod -R 755 /srv/repo/public
    
    log_success "Assets deployed"
}

# Generates the installer script
generate_install_script() {
    local gpg_key_file=$1
    local install_script_path="${WEB_ROOT_DIR}/install.sh"
    local tmp_install_script
    tmp_install_script=$(mktemp)
    
    cat > "${tmp_install_script}" <<'INSTALL_EOF'
#!/bin/sh
#
# Installer for SC Console Agent
#
# This script automatically detects the OS distribution and installs the agent
# from the appropriate APT repository.
#
set -e

REPO_HOST="repo.cloud.strettch.com"
REPO_PATH="REPO_URL_PATH_PLACEHOLDER"
PACKAGE_NAME="sc-console-agent"
GPG_KEY_FILENAME="GPG_KEY_PLACEHOLDER"

# Check for root privileges
if [ "$(id -u)" -ne "0" ]; then
    echo "Error: This script must be run as root." >&2
    exit 1
fi

# Logging function
log_and_run() {
    echo "   -> $1"
    if ! eval "$2"; then
        echo "Error: Failed to $3" >&2
        exit 1
    fi
}

# Detect distribution
if [ ! -f /etc/os-release ]; then
    echo "Error: Cannot detect OS from /etc/os-release" >&2
    exit 1
fi
DISTRIBUTION=$(. /etc/os-release && echo "$VERSION_CODENAME")

# Handle unsupported/EOL distributions
case "$DISTRIBUTION" in
    bionic|focal|jammy|noble)
        # Supported distributions
        ;;
    oracular)
        echo "Note: Ubuntu 24.10 (oracular) has reached end-of-life. Using noble (24.04 LTS) repository."
        DISTRIBUTION="noble"
        ;;
    *)
        echo "Warning: $DISTRIBUTION is not explicitly supported, falling back to noble (Ubuntu 24.04)"
        DISTRIBUTION="noble"
        ;;
esac

echo "--- Installing ${PACKAGE_NAME} for ${DISTRIBUTION} ---"

APT_LOCK_TIMEOUT="${SC_APT_LOCK_TIMEOUT:-300}"
APT_MAX_RETRIES="${SC_APT_MAX_RETRIES:-5}"
APT_RETRY_DELAY="${SC_APT_RETRY_DELAY:-15}"

apt_get_with_retry() {
    local apt_args="$1"
    local attempt=1

    while [ "$attempt" -le "$APT_MAX_RETRIES" ]; do
        if eval "apt-get -o DPkg::Lock::Timeout=$APT_LOCK_TIMEOUT $apt_args"; then
            return 0
        fi

        if [ "$attempt" -lt "$APT_MAX_RETRIES" ]; then
            echo "   -> APT command failed (attempt ${attempt}/${APT_MAX_RETRIES}), retrying in ${APT_RETRY_DELAY}s"
            sleep "$APT_RETRY_DELAY"
        fi

        attempt=$((attempt + 1))
    done

    return 1
}

# Clean up old configurations
echo "-> Cleaning up old repository configurations..."
rm -f /etc/apt/sources.list.d/sc-agent.list \
      /usr/share/keyrings/sc-agent-keyring.gpg \
      /etc/apt/sources.list.d/${PACKAGE_NAME}.list \
      /usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg 2>/dev/null || true

# Install prerequisites
log_and_run "Updating package lists" "apt_get_with_retry 'update > /dev/null'" "update package lists"
log_and_run "Installing prerequisites" "apt_get_with_retry 'install -y apt-transport-https ca-certificates curl gnupg > /dev/null'" "install prerequisites"

# Add repository key and source
KEYRING_FILE="/usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg"
log_and_run "Downloading GPG key" "curl -fsSL \"https://${REPO_HOST}/${REPO_PATH}/${GPG_KEY_FILENAME}\" | gpg --dearmor | tee \"${KEYRING_FILE}\" >/dev/null" "download GPG key"

SOURCES_FILE="/etc/apt/sources.list.d/${PACKAGE_NAME}.list"
log_and_run "Adding repository" "echo \"deb [signed-by=${KEYRING_FILE}] https://${REPO_HOST}/${REPO_PATH}/aptly ${DISTRIBUTION} main\" | tee \"${SOURCES_FILE}\" >/dev/null" "add repository"

# Update and install
log_and_run "Updating package index" "apt_get_with_retry \"update -o Dir::Etc::SourceList='${SOURCES_FILE}' -o Dir::Etc::SourceParts='-' -o APT::Get::List-Cleanup='0' > /dev/null\"" "update package index"
log_and_run "Installing ${PACKAGE_NAME}" "apt_get_with_retry 'install -y \"${PACKAGE_NAME}\"'" "install ${PACKAGE_NAME}"

echo "✅ ${PACKAGE_NAME} installed successfully!"
exit 0
INSTALL_EOF
    
    # Replace placeholders
    sed -i "s|REPO_URL_PATH_PLACEHOLDER|${REPO_URL_PATH}|g" "${tmp_install_script}"
    sed -i "s|GPG_KEY_PLACEHOLDER|${gpg_key_file}|g" "${tmp_install_script}"
    
    sudo mv "${tmp_install_script}" "${install_script_path}"
    sudo chmod +x "${install_script_path}"
    
    log_success "Install script generated"
}

# Validates the deployment
validate_deployment() {
    log_info "Validating deployment..."
    local validation_passed=true
    
    # Check install.sh exists and is executable
    if [[ ! -f "${WEB_ROOT_DIR}/install.sh" ]]; then
        log_error "install.sh was not created"
        validation_passed=false
    elif [[ ! -x "${WEB_ROOT_DIR}/install.sh" ]]; then
        log_error "install.sh is not executable"
        validation_passed=false
    fi
    
    # Check GPG key exists
    if [[ ! -f "${WEB_ROOT_DIR}/${PACKAGE_NAME}-repo.gpg" ]]; then
        log_error "GPG public key was not created"
        validation_passed=false
    fi
    
    # Check aptly directory exists and is not empty
    if [[ ! -d "${WEB_ROOT_DIR}/aptly" ]] || [[ -z "$(ls -A "${WEB_ROOT_DIR}/aptly")" ]]; then
        log_error "Aptly directory is empty or missing"
        validation_passed=false
    fi
    
    # Verify install.sh has correct domain
    if ! grep -q "REPO_HOST=\"${REPO_DOMAIN}\"" "${WEB_ROOT_DIR}/install.sh"; then
        log_error "install.sh has incorrect domain"
        validation_passed=false
    fi
    
    if [[ "$validation_passed" = true ]]; then
        log_success "All validation checks passed!"
        return 0
    else
        log_error "Deployment validation failed"
        return 1
    fi
}

# --- Main Execution Function ---
main() {
    # Initialize variables and configuration
    setup_variables
    get_package_version
    
    # Step 1: Check dependencies
    log_step 1 8 "Checking dependencies"
    check_dependencies
    
    # Step 2: Clean up previous builds
    log_step 2 8 "Cleaning up previous builds"
    cleanup_previous_builds
    
    # Step 3: Verify packaging files
    log_step 3 8 "Verifying packaging files"
    verify_required_files
    
    # Step 4: Build Go binaries
    log_step 4 8 "Building Go binaries"
    build_go_binaries
    
    # Step 5: Create Debian packages
    log_step 5 8 "Creating Debian packages"
    build_deb_packages
    
    # Step 6: Manage APT repository
    log_step 6 8 "Managing APT repository"
    manage_aptly_repository
    publish_repository
    
    # Step 7: Deploy assets
    log_step 7 8 "Deploying repository assets"
    deploy_repository_assets
    
    # Step 8: Validate deployment
    log_step 8 8 "Validating deployment"
    validate_deployment
    
    # Print summary
    echo ""
    echo "========================================================================"
    echo "✅ Repository setup complete!"
    echo "📦 Repository type: ${RELEASE_TYPE}"
    echo "🏷️  Version deployed: ${PACKAGE_VERSION}"
    echo "🏗️  Architectures: ${ARCHES}"
    echo "📍 Repository URL: https://${REPO_DOMAIN}/${REPO_URL_PATH}/"
    echo "🔧 Install command:"
    echo "   curl -sSL https://${REPO_DOMAIN}/${REPO_URL_PATH}/install.sh | sudo bash"
    echo "========================================================================"
}

# Run the main function with all arguments
main "$@"