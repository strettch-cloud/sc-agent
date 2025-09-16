#!/usr/bin/env bash
# generate-changelog.sh - Generate a categorized changelog
set -euo pipefail
IFS=$'\n\t'

CHANNEL="${1:-}"
MAX_COMMITS=50

if [[ -z "$CHANNEL" ]]; then
    echo "Usage: $0 {beta|stable}"
    exit 1
fi

# Ensure tags are up to date
git fetch --tags >/dev/null 2>&1 || true

# Get last tag depending on channel
if [[ "$CHANNEL" == "stable" ]]; then
    LAST_TAG=$(git tag -l --sort=-version:refname | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || echo "")
else
    LAST_TAG=$(git tag -l --sort=-version:refname | head -1 || echo "")
fi

# Format commit messages
format_commit() {
    local msg="$1"
    local hash="$2"

    # Skip merge commits and [skip ci]
    if [[ "$msg" =~ ^Merge ]] || [[ "$msg" =~ \[skip\ ci\] ]]; then
        return
    fi

    if [[ "$msg" =~ ^feat:|^feature: ]]; then
        echo "✨ ${msg#*:} (${hash})"
    elif [[ "$msg" =~ ^fix:|^bugfix: ]]; then
        echo "🐛 ${msg#*:} (${hash})"
    elif [[ "$msg" =~ ^docs: ]]; then
        echo "📚 ${msg#*:} (${hash})"
    elif [[ "$msg" =~ ^test: ]]; then
        echo "🧪 ${msg#*:} (${hash})"
    elif [[ "$msg" =~ ^refactor: ]]; then
        echo "♻️ ${msg#*:} (${hash})"
    elif [[ "$msg" =~ ^perf: ]]; then
        echo "⚡ ${msg#*:} (${hash})"
    else
        echo "- ${msg} (${hash})"
    fi
}

# Print header
if [[ -z "$LAST_TAG" ]]; then
    echo "## 🎉 Initial Release"
else
    echo "## 📦 Changes since ${LAST_TAG}"
fi
echo

# Group commits
declare -a features=() fixes=() other=()

while IFS= read -r line; do
    hash=$(echo "$line" | cut -d' ' -f1)
    msg=$(echo "$line" | cut -d' ' -f2-)
    formatted=$(format_commit "$msg" "$hash")
    
    if [[ -n "${formatted:-}" ]]; then
        if [[ "$formatted" =~ ^✨ ]]; then
            features+=("$formatted")
        elif [[ "$formatted" =~ ^🐛 ]]; then
            fixes+=("$formatted")
        else
            other+=("$formatted")
        fi
    fi
done < <(if [[ -z "$LAST_TAG" ]]; then
    git log --oneline --pretty=format:"%h %s" HEAD -${MAX_COMMITS}
else
    git log --oneline --pretty=format:"%h %s" "${LAST_TAG}..HEAD"
fi)

# Output categorized commits
if ((${#features[@]} > 0)); then
    echo "### ✨ New Features"
    printf '%s\n' "${features[@]}"
    echo
fi

if ((${#fixes[@]} > 0)); then
    echo "### 🐛 Bug Fixes"
    printf '%s\n' "${fixes[@]}"
    echo
fi

if ((${#other[@]} > 0)); then
    echo "### 🔧 Other Changes"
    printf '%s\n' "${other[@]}"
    echo
fi

# Contributors
echo "### 👥 Contributors"

# Method 1: Use email to derive GitHub usernames (if your team uses GitHub email format)
# This works if commits use username@users.noreply.github.com format
get_contributors_from_email() {
    local range="$1"
    git log --pretty=format:"%ae" $range | \
        grep -E "^[^@]+@users\.noreply\.github\.com$" | \
        sed 's/@users\.noreply\.github\.com$//' | \
        sed 's/^[0-9]*+//' | \
        sort -u | \
        sed 's/^/- @/'
}

# Method 2: Use author names without @ symbol (safer approach)
get_contributors_by_name() {
    local range="$1"
    git log --pretty=format:"%an" $range | \
        sort -u | \
        sed 's/^/- /'
}

# Method 3: Use Co-authored-by trailers if present
get_contributors_from_trailers() {
    local range="$1"
    git log --pretty=format:"%b" $range | \
        grep -E "^Co-authored-by:" | \
        sed 's/Co-authored-by: //' | \
        sed 's/ <.*$//' | \
        sort -u | \
        sed 's/^/- /'
}

# Determine the range for contributors
if [[ -z "$LAST_TAG" ]]; then
    RANGE="HEAD -${MAX_COMMITS}"
else
    RANGE="${LAST_TAG}..HEAD"
fi

# Try to get GitHub usernames from email first
GITHUB_CONTRIBUTORS=$(get_contributors_from_email "$RANGE")

if [[ -n "$GITHUB_CONTRIBUTORS" ]]; then
    # We found GitHub usernames from emails
    echo "$GITHUB_CONTRIBUTORS"
else
    # Fallback to author names
    get_contributors_by_name "$RANGE"
fi

# Also include co-authors if any
CO_AUTHORS=$(get_contributors_from_trailers "$RANGE")
if [[ -n "$CO_AUTHORS" ]]; then
    echo ""
    echo "#### Co-authors"
    echo "$CO_AUTHORS"
fi

echo
echo

# Installation instructions
echo "### 📥 Installation"
echo '```bash'
if [[ "$CHANNEL" == "beta" ]]; then
    echo 'curl -sSL https://repo.cloud.strettch.com/console/beta/install.sh | sudo bash'
else
    echo 'curl -sSL https://repo.cloud.strettch.com/console/install.sh | sudo bash'
fi
echo '```'