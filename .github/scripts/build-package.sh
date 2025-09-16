#!/bin/bash
# scripts/build-package.sh - Package building helper
set -euo pipefail

VERSION="${1:?Version required}"
CHANNEL="${2:-stable}"
PACKAGE_NAME="sc-console-agent"

echo "Building package: ${PACKAGE_NAME} v${VERSION} (${CHANNEL})"

# Create staging directory
STAGING_DIR="/tmp/${PACKAGE_NAME}-staging"
rm -rf "$STAGING_DIR"
mkdir -p "${STAGING_DIR}/usr/bin" "${STAGING_DIR}/etc/systemd/system"

# Copy binaries
cp "build/${PACKAGE_NAME}" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}"

# Copy updater script
cp "packaging/scripts/${PACKAGE_NAME}-updater.sh" "${STAGING_DIR}/usr/bin/"
chmod +x "${STAGING_DIR}/usr/bin/${PACKAGE_NAME}-updater.sh"

# Copy systemd files
cp "packaging/systemd/${PACKAGE_NAME}.service" "${STAGING_DIR}/etc/systemd/system/"
cp "packaging/systemd/${PACKAGE_NAME}-update-scheduler.service" "${STAGING_DIR}/etc/systemd/system/"
cp "packaging/systemd/${PACKAGE_NAME}-update-scheduler.timer" "${STAGING_DIR}/etc/systemd/system/"

# Build .deb package
DESCRIPTION="SC Console Agent - Secure access management"
[ "$CHANNEL" = "beta" ] && DESCRIPTION="$DESCRIPTION (Beta)"

fpm -s dir -t deb \
    -n "$PACKAGE_NAME" \
    -v "$VERSION" \
    -C "$STAGING_DIR/" \
    --description "$DESCRIPTION" \
    --maintainer "engineering@strettch.com" \
    --url "https://github.com/strettch/sc-console-agent" \
    --depends "libc6" \
    --depends "dmidecode" \
    --depends "jq" \
    --depends "curl" \
    --after-install "packaging/scripts/post-install.sh" \
    --before-remove "packaging/scripts/pre-remove.sh" \
    --log error

echo "Package built: ${PACKAGE_NAME}_${VERSION}_amd64.deb"