#!/bin/bash

set -e

RELEASE_TYPE="$1"

if [[ "$RELEASE_TYPE" != "beta" && "$RELEASE_TYPE" != "stable" ]]; then
    echo "Usage: $0 {beta|stable}"
    exit 1
fi

# Get all tags and sort them
git fetch --tags >/dev/null 2>&1 || true

generate_commit_list() {
    local from_ref="$1"
    local to_ref="$2"
    local title="$3"
    
    if [[ -z "$from_ref" ]]; then
        # No previous tag, get all commits
        commits=$(git log --oneline --pretty=format:"- %s (%h)" "$to_ref")
    else
        # Get commits between two refs
        commits=$(git log --oneline --pretty=format:"- %s (%h)" "${from_ref}..${to_ref}")
    fi
    
    if [[ -n "$commits" ]]; then
        echo "### $title"
        echo ""
        echo "$commits"
        echo ""
    fi
}

generate_contributors() {
    local from_ref="$1"
    local to_ref="$2"
    
    if [[ -z "$from_ref" ]]; then
        # No previous tag, get all contributors
        raw_contributors=$(git log --pretty=format:"%an|%ae" "$to_ref" | sort -u)
    else
        # Get contributors between two refs
        raw_contributors=$(git log --pretty=format:"%an|%ae" "${from_ref}..${to_ref}" | sort -u)
    fi
    
    if [[ -n "$raw_contributors" ]]; then
        echo "### Contributors"
        echo ""
        while IFS='|' read -r name email; do
            # Convert GitHub noreply emails to @username format
            if [[ "$email" =~ ^[0-9]+\+([^@]+)@users\.noreply\.github\.com$ ]]; then
                username="${BASH_REMATCH[1]}"
                echo "- $name (@$username)"
            elif [[ "$email" =~ ^([^@]+)@users\.noreply\.github\.com$ ]]; then
                username="${BASH_REMATCH[1]}"
                echo "- $name (@$username)"
            else
                # For non-GitHub emails, just show the name
                echo "- $name"
            fi
        done <<< "$raw_contributors"
        echo ""
    fi
}

if [[ "$RELEASE_TYPE" == "beta" ]]; then
    # For beta releases, show commits since last beta or last stable
    
    LAST_BETA=$(git tag -l --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]+$' | head -1 || echo "")
    LAST_STABLE=$(git tag -l --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")
    
    # Determine the reference point
    if [[ -n "$LAST_BETA" ]]; then
        FROM_REF="$LAST_BETA"
        FROM_TYPE="beta"
    elif [[ -n "$LAST_STABLE" ]]; then
        FROM_REF="$LAST_STABLE"
        FROM_TYPE="stable"
    else
        FROM_REF=""
        FROM_TYPE="initial"
    fi
    
    if [[ "$FROM_TYPE" == "initial" ]]; then
        generate_commit_list "$FROM_REF" "HEAD" "All Changes"
    else
        generate_commit_list "$FROM_REF" "HEAD" "Changes since $FROM_REF"
    fi
    
    generate_contributors "$FROM_REF" "HEAD"
    
elif [[ "$RELEASE_TYPE" == "stable" ]]; then
    # For stable releases, show comprehensive changelog since last stable
    
    LAST_STABLE=$(git tag -l --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")
    
    if [[ -n "$LAST_STABLE" ]]; then
        # Get all commits since last stable (including any beta releases)
        generate_commit_list "$LAST_STABLE" "HEAD" "Changes since $LAST_STABLE"
        
        # Show beta releases included in this stable release
        BETA_RELEASES=$(git tag -l --sort=version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]+$' | while read -r tag; do
            # Check if this beta is after the last stable
            if git merge-base --is-ancestor "$LAST_STABLE" "$tag" 2>/dev/null; then
                echo "$tag"
            fi
        done)
        
        if [[ -n "$BETA_RELEASES" ]]; then
            echo "### Beta Releases Included"
            echo ""
            while IFS= read -r beta; do
                if [[ -n "$beta" ]]; then
                    echo "- $beta"
                fi
            done <<< "$BETA_RELEASES"
            echo ""
        fi
        
    else
        # First stable release
        generate_commit_list "" "HEAD" "All Changes"
    fi
    
    generate_contributors "$LAST_STABLE" "HEAD"
    
    # Add installation instructions
    echo "### Installation"
    echo ""
    echo '```bash'
    echo 'curl -sSL https://sc.repo.cloud.strettch.dev/install.sh | sudo bash'
    echo '```'
    echo ""
fi

# Add footer
echo "---"
echo ""
echo "*Generated automatically by GitHub Actions*"