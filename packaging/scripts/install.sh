#!/bin/bash
# SC-Agent Installation Script
# Purpose: Add package repository and install sc-agent via APT
# Usage: curl -sSL https://repo.cloud.strettch.dev/install.sh | sudo bash

set -e  # Exit immediately if any command fails

# Repository configuration - can be overridden with environment variable
PACKAGE_REPO_URL="${SC_REPO_URL:-https://repo.cloud.strettch.dev/console}"
PACKAGE_NAME="sc-agent"

# Check if running on Debian/Ubuntu (only supported systems)
if [ ! -f /etc/debian_version ]; then
    echo "Error: This installer only supports Debian/Ubuntu systems"
    echo "Detected OS does not have /etc/debian_version"
    exit 1
fi

echo "Installing SC-Agent on Debian/Ubuntu system..."

# Download and install GPG key for package verification
# -fsSL flags: fail silently, silent, show errors, follow redirects
echo "Adding repository GPG key..."
curl -fsSL "${PACKAGE_REPO_URL}/gpg.key" | gpg --dearmor | tee /usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg > /dev/null

# Add repository to APT sources
# signed-by ensures only packages signed with our GPG key are accepted
echo "Adding repository to APT sources..."
echo "deb [signed-by=/usr/share/keyrings/${PACKAGE_NAME}-keyring.gpg] ${PACKAGE_REPO_URL}/apt stable main" > /etc/apt/sources.list.d/${PACKAGE_NAME}.list

# Update package index to include our new repository
echo "Updating package index..."
apt-get update

# Install the agent package
# -y flag: automatic yes to prompts
echo "Installing ${PACKAGE_NAME} package..."
apt-get install -y ${PACKAGE_NAME}

echo "SC-Agent installed successfully"
echo "Run 'systemctl status ${PACKAGE_NAME}' to check service status"