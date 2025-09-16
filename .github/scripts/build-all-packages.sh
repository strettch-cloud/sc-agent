#!/bin/bash
# scripts/build-all-packages.sh - Build multiple package formats
set -euo pipefail

VERSION="${1:?Version required}"
CHANNEL="${2:-stable}"

# Build for multiple architectures and formats
for arch in amd64 arm64; do
    echo "Building for $arch..."
    
    # Ensure binary exists for architecture
    if [ ! -f "build/sc-console-agent-linux-$arch" ]; then
        GOOS=linux GOARCH=$arch make build VERSION="$VERSION"
        mv build/sc-console-agent "build/sc-console-agent-linux-$arch"
    fi
    
    # Build DEB
    ./scripts/build-package.sh "$VERSION" "$CHANNEL"
    mv "sc-console-agent_${VERSION}_amd64.deb" \
       "sc-console-agent_${VERSION}_${arch}.deb"
    
    # Build RPM (if on amd64)
    if [ "$arch" = "amd64" ] && command -v fpm >/dev/null; then
        ./scripts/build-rpm.sh "$VERSION" "$CHANNEL"
    fi
done

# Generate checksums
sha256sum *.deb *.rpm 2>/dev/null > packages.sha256 || \
sha256sum *.deb > packages.sha256

echo "All packages built successfully"