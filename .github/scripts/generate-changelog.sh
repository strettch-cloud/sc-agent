#!/bin/bash
set -e

CHANNEL="$1"

# Get last stable release tag
LAST_TAG=$(git tag -l --sort=-version:refname | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")

# Generate commits with hashes
if [ -z "$LAST_TAG" ]; then
    echo "### Initial Release"
    echo ""
    git log --oneline --pretty=format:"- %s (%h)" HEAD | head -20
else
    echo "### Changes since $LAST_TAG"
    echo ""
    git log --oneline --pretty=format:"- %s (%h)" "$LAST_TAG"..HEAD
fi
echo ""

# Generate contributors
echo "### Contributors"
echo ""
if [ -z "$LAST_TAG" ]; then
    git log --pretty=format:"%an" HEAD | sort -u | sed 's/^/- /'
else
    git log --pretty=format:"%an" "$LAST_TAG"..HEAD | sort -u | sed 's/^/- /'
fi
echo ""

# Add installation instructions
echo "### Installation"
echo ""
echo '```bash'
echo 'curl -sSL https://repo.cloud.strettch.com/console/install.sh | sudo bash'
echo '```'