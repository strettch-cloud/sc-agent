# SC-Agent

Strettch Cloud Agent for SSH key management and system monitoring.

## Installation

Install SC-Agent using the official repository:

```bash
curl -sSL https://repo.cloud.strettch.dev/console/install.sh | sudo bash
```

## Service Management

### Check service status
```bash
sudo systemctl status sc-agent
```

### Start/stop service
```bash
sudo systemctl start sc-agent
sudo systemctl stop sc-agent
sudo systemctl restart sc-agent
```

### Enable/disable service (start on boot)
```bash
sudo systemctl enable sc-agent   # Enable auto-start on boot
sudo systemctl disable sc-agent  # Disable auto-start
```

### View logs
```bash
sudo journalctl -u sc-agent -f          # Follow live logs
sudo journalctl -u sc-agent --since=1h  # Last hour logs
```

## Auto-Update

### Check auto-update timer status
```bash
sudo systemctl status sc-agent-update-scheduler.timer
```

### Manual update
```bash
sudo systemctl start sc-agent-updater
```

### Auto-update logs
```bash
sudo journalctl -u sc-agent-updater -f
sudo journalctl -u sc-agent-update-scheduler.timer
```

## Configuration

Configuration file: `/etc/sc-agent/config.yaml`

```bash
sudo nano /etc/sc-agent/config.yaml
sudo systemctl restart sc-agent  # Restart to apply changes
```

## Removal

### Remove package (keep configuration)
```bash
sudo apt-get remove sc-agent
```

### Complete removal (remove package and all configurations)
```bash
sudo apt-get purge sc-agent
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