#!/bin/bash
# Simple Build Test Script
# Purpose: Test building packages without requiring root access
set -e

ORG_NAME="strettch"
GPG_EMAIL="engineering@strettch.com"
PACKAGE_NAME="sc-console-agent"
PACKAGE_VERSION=$(git describe --tags --always --dirty)

# Build for Linux (needed for .deb packages)
echo "Building Linux binary..."
GOOS=linux GOARCH=amd64 make build

# Create staging directory
STAGING_DIR="/tmp/${PACKAGE_NAME}-build-test"
rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}/usr/bin"
mkdir -p "${STAGING_DIR}/etc/systemd/system"

# Copy files
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"

cp "packaging/scripts/${PACKAGE_NAME}-updater.sh" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"

cp "packaging/systemd/sc-console-agent.service" "${STAGING_DIR}/etc/systemd/system/"
cp "packaging/systemd/sc-console-agent-update-scheduler.service" "${STAGING_DIR}/etc/systemd/system/"
cp "packaging/systemd/sc-console-agent-update-scheduler.timer" "${STAGING_DIR}/etc/systemd/system/"

# Test package creation
echo "Testing .deb package creation..."
fpm -s dir -t deb \
    -n ${PACKAGE_NAME} \
    -v ${PACKAGE_VERSION#v} \
    -C "${STAGING_DIR}/" \
    --description "SC Agent for SSH access management" \
    --maintainer "${GPG_EMAIL}" \
    --url "https://github.com/strettch/sc-console-agent" \
    --depends "systemd" \
    --after-install "packaging/scripts/post-install.sh" \
    --before-remove "packaging/scripts/pre-remove.sh" \
    --deb-systemd "packaging/systemd/sc-console-agent.service" \
    --deb-no-default-config-files

echo "✅ Package built successfully: ${PACKAGE_NAME}_${PACKAGE_VERSION#v}_amd64.deb"
echo "Package info:"
dpkg-deb --info "${PACKAGE_NAME}_${PACKAGE_VERSION#v}_amd64.deb"