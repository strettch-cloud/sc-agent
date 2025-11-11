# SC-Agent

[Strettch Cloud Console Agent] Securely bridging your VMs with the cloud control plane for real-time console access.

## Installation

Install SC-Agent using the official APT repository.

### 1. Install prerequisites
```bash
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg
```

### 2. Add repository GPG key
```bash
curl -fsSL https://repo.cloud.strettch.com/console/sc-console-agent-repo.gpg | sudo gpg --dearmor -o /usr/share/keyrings/sc-console-agent-keyring.gpg
```

### 3. Add APT repository
```bash
echo "deb [signed-by=/usr/share/keyrings/sc-console-agent-keyring.gpg] https://repo.cloud.strettch.com/console/aptly $(. /etc/os-release && echo "$VERSION_CODENAME") main" | sudo tee /etc/apt/sources.list.d/sc-console-agent.list
```

### 4. Install the agent
```bash
sudo apt-get update
sudo apt-get install -y sc-console-agent
```

## Service Management

### Check service status
```bash
sudo systemctl status sc-console-agent
```

### Start/stop service
```bash
sudo systemctl start sc-console-agent
sudo systemctl stop sc-console-agent
sudo systemctl restart sc-console-agent
```

### Enable/disable service (start on boot)
```bash
sudo systemctl enable sc-console-agent   # Enable auto-start on boot
sudo systemctl disable sc-console-agent  # Disable auto-start
```

### View logs
```bash
sudo journalctl -u sc-console-agent -f          # Follow live logs
sudo journalctl -u sc-console-agent --since=1h  # Last hour logs
```

## Auto-Update

### Check auto-update timer status
```bash
sudo systemctl status sc-console-agent-update-scheduler.timer
```

### Manual update
```bash
sudo systemctl start sc-console-agent-updater
```

### Auto-update logs
```bash
sudo journalctl -u sc-console-agent-updater -f
sudo journalctl -u sc-console-agent-update-scheduler.timer
```

## Configuration

Configuration file: `/etc/sc-console-agent/config.yaml`

```bash
sudo nano /etc/sc-console-agent/config.yaml
sudo systemctl restart sc-console-agent  # Restart to apply changes
```

## Removal

### Remove package (keep configuration)
```bash
sudo apt-get remove sc-console-agent
```

### Complete removal (remove package and all configurations)
```bash
sudo apt-get purge sc-console-agent
```

## Development

### Build from source
```bash
make build
make build-all  # Multi-platform builds
```

### Run locally
```bash
make run
make run-debug  # With debug logging
```