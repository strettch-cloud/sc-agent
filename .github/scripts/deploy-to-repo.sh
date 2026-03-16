#!/bin/bash
# scripts/deploy-to-repo.sh - Deploy to repository server
set -euo pipefail

VERSION="${1:?Version required}"
CHANNEL="${2:?Channel required}"
DEPLOY_KEY="${3:?Deploy key required}"
REPO_HOST="${4:?Repository host required}"
REPO_USER="${5:?Repository user required}"
GIT_REF="${6:-refs/heads/main}"
GIT_SHA="${7:-}"

# Setup SSH securely
setup_ssh() {
    mkdir -p ~/.ssh
    echo "$DEPLOY_KEY" > ~/.ssh/deploy_key
    chmod 600 ~/.ssh/deploy_key
    
    # Add host key verification
    ssh-keyscan -H "$REPO_HOST" >> ~/.ssh/known_hosts 2>/dev/null
    
    # Create SSH config for this connection
    cat > ~/.ssh/config <<-EOF
	Host repo-server
	    HostName $REPO_HOST
	    User $REPO_USER
	    IdentityFile ~/.ssh/deploy_key
	    StrictHostKeyChecking yes
	    ConnectTimeout 10
	    ServerAliveInterval 60
	EOF
}

# Deploy to repository
deploy() {
    echo "Deploying version $VERSION to $CHANNEL repository..."
    
    # Update repository code
    ssh repo-server bash -s <<-REMOTE_SCRIPT
		set -euo pipefail
		
		cd /root/sc-console-agent || exit 1
		
		# Update git repository to the exact workflow revision
		echo "Updating repository to ${GIT_REF}${GIT_SHA:+ @ ${GIT_SHA}}..."
		git fetch origin --tags
		if [ -n "${GIT_SHA}" ]; then
		    git fetch origin "${GIT_SHA}"
		    git checkout --detach "${GIT_SHA}"
		elif [ -n "${GIT_REF}" ]; then
		    git fetch origin "${GIT_REF}"
		    git checkout -B deploy-target FETCH_HEAD
		else
		    git checkout main
		    git reset --hard origin/main
		fi
		
		# Clean up old tags
		git tag -d v* 2>/dev/null || true
		
		# Run setup with proper environment
		echo "Building and publishing packages..."
		export RELEASE_TYPE="$CHANNEL"
		export PACKAGE_VERSION="$VERSION"
		
		if ./setup_repo.sh; then
		    echo "✅ Deployment successful"
		else
		    echo "❌ Deployment failed"
		    exit 1
		fi
		
		# Validate deployment
		if [ "$CHANNEL" = "beta" ]; then
		    INSTALL_PATH="/srv/repo/public/console/beta/install.sh"
		else
		    INSTALL_PATH="/srv/repo/public/console/install.sh"
		fi
		
		if [ ! -f "\$INSTALL_PATH" ]; then
		    echo "❌ Install script not found at \$INSTALL_PATH"
		    exit 1
		fi
		
		echo "✅ All validations passed"
	REMOTE_SCRIPT
}

# Cleanup function
cleanup() {
    rm -f ~/.ssh/deploy_key
    rm -f ~/.ssh/config
}

# Main execution
trap cleanup EXIT
setup_ssh
deploy

echo "Deployment completed for $VERSION ($CHANNEL)"