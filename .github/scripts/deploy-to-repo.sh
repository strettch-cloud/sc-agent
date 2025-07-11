#!/bin/bash

set -e

REPO_TYPE="$1"  # "beta" or "stable" (not used since we use existing setup_repo.sh)
PACKAGE_FILE="$2"

if [[ ! -f "$PACKAGE_FILE" ]]; then
    echo "Error: Package file $PACKAGE_FILE not found"
    exit 1
fi

# Validate required environment variables
required_vars=("REPO_SSH_KEY" "REPO_HOST" "REPO_USER")
for var in "${required_vars[@]}"; do
    if [[ -z "${!var}" ]]; then
        echo "Error: Environment variable $var is required"
        exit 1
    fi
done

echo "Deploying $PACKAGE_FILE using existing setup_repo.sh..."

# Setup SSH key
mkdir -p ~/.ssh
echo "$REPO_SSH_KEY" > ~/.ssh/deploy_key
chmod 600 ~/.ssh/deploy_key
ssh-keyscan -H "$REPO_HOST" >> ~/.ssh/known_hosts

# Copy package to server (replaces the existing one)
echo "Uploading package to repository server..."
scp -i ~/.ssh/deploy_key "$PACKAGE_FILE" "${REPO_USER}@${REPO_HOST}:/root/sc-agent/"

# Run the existing setup_repo.sh script on the server
echo "Running setup_repo.sh on repository server..."
ssh -i ~/.ssh/deploy_key "${REPO_USER}@${REPO_HOST}" "cd /root/sc-agent && ./setup_repo.sh"

# Cleanup
rm -f ~/.ssh/deploy_key

echo "✅ Deployment completed successfully using setup_repo.sh!"
echo "📦 Repository updated at: https://sc.repo.cloud.strettch.dev/"
echo "🚀 Install command: curl -sSL https://sc.repo.cloud.strettch.dev/install.sh | sudo bash"