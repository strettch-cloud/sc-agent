#!/bin/bash
# Systemd Configuration Test Script
# Purpose: Validate systemd files and script configurations
set -e

echo "=== Testing Systemd Configuration ==="

# Check that all required files exist
echo "Checking systemd files..."
FILES=(
    "packaging/systemd/sc-agent.service"
    "packaging/systemd/sc-agent-update-scheduler.service" 
    "packaging/systemd/sc-agent-update-scheduler.timer"
    "packaging/scripts/post-install.sh"
    "packaging/scripts/pre-remove.sh"
    "packaging/scripts/sc-agent-updater.sh"
)

for file in "${FILES[@]}"; do
    if [[ -f "$file" ]]; then
        echo "✅ $file exists"
    else
        echo "❌ $file missing"
        exit 1
    fi
done

# Validate systemd file syntax
echo -e "\nValidating systemd syntax..."

# Check if systemd-analyze is available (Linux only)
if command -v systemd-analyze &> /dev/null; then
    for service in packaging/systemd/*.service packaging/systemd/*.timer; do
        echo "Checking $service..."
        systemd-analyze verify "$service" || echo "⚠️ Syntax issues in $service"
    done
else
    echo "ℹ️ systemd-analyze not available (not on Linux), skipping syntax validation"
fi

# Check script syntax
echo -e "\nChecking bash script syntax..."
for script in packaging/scripts/*.sh; do
    echo "Checking $script..."
    bash -n "$script" && echo "✅ $script syntax OK" || echo "❌ $script has syntax errors"
done

# Verify service name consistency
echo -e "\nChecking service name consistency..."

# Check timer references correct service
timer_requires=$(grep "Requires=" packaging/systemd/sc-agent-update-scheduler.timer | cut -d'=' -f2)
expected_service="sc-agent-update-scheduler.service"

if [[ "$timer_requires" == "$expected_service" ]]; then
    echo "✅ Timer correctly references $expected_service"
else
    echo "❌ Timer references '$timer_requires' but should reference '$expected_service'"
fi

# Check script service names match
post_install_services=$(grep -E "(SERVICE_NAME|UPDATER_SERVICE|UPDATER_TIMER)" packaging/scripts/post-install.sh)
pre_remove_services=$(grep -E "(SERVICE_NAME|UPDATER_SERVICE|UPDATER_TIMER)" packaging/scripts/pre-remove.sh)

echo -e "\nService names in scripts:"
echo "post-install.sh:"
echo "$post_install_services"
echo -e "\npre-remove.sh:" 
echo "$pre_remove_services"

echo -e "\n=== Configuration Test Complete ==="