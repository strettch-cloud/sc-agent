# SC Console Agent

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

The SC Console Agent (`sc-console-agent`) runs on [Strettch Cloud](https://cloud.strettch.com)
virtual machines and gives their owners browser-based console access without opening SSH to
the internet or storing long-lived keys on the machine.

## How it works

```
 Strettch Cloud control plane                      Virtual machine
 ────────────────────────────                      ───────────────
 user opens a console session
        │
        │ 1. signed "knock" (UDP 9999)  ───────────►  sc-console-agent
        │    resource ID + timestamp, ed25519          verifies the signature with the
        │                                              platform key from the metadata service
        │
        │ 2. agent asks for the session key  ◄──────  authenticated with a short-lived token
        │                                              from the metadata service
        │
        │ 3. short-lived SSH key  ──────────────────►  added to root's authorized_keys,
        │                                              tagged as console-managed
        ▼
 console session over SSH                          console keys are removed every 2 minutes;
                                                   your own keys are never touched
```

- **Knock verification:** a knock is accepted only if it carries a valid ed25519 signature over
  this machine's resource ID and a recent timestamp (`signature_timeout`, 30 s by default), so
  replayed or forged packets are dropped.
- **Credentials:** the agent holds no secrets on disk. It gets a short-lived token, the
  platform's public key and the API address from the link-local metadata service.
- **Short-lived access:** console keys are removed from `authorized_keys` every
  `key_cleanup_interval` (2 minutes by default), so a console key can only be used to start a
  session for a short time; sessions already open are not interrupted.
- **Heartbeat:** the agent reports its version to the platform every 5 minutes.

## Installation

On an Ubuntu VM, as root:

```bash
curl -sSL https://repo.cloud.strettch.com/console/install.sh | sudo sh
```

This adds the signed APT repository, installs the package and starts two systemd units:

| Unit | Purpose |
|---|---|
| `sc-console-agent.service` | The agent |
| `sc-console-agent-update-scheduler.timer` | Checks for a new version every 30 minutes and installs it |

To try pre-release builds, use `https://repo.cloud.strettch.com/console/beta/install.sh` instead.

Supported: Ubuntu 18.04, 20.04, 22.04 and 24.04 on `amd64` and `arm64`.

## Operating the agent

```bash
sudo systemctl status sc-console-agent                       # status
sudo journalctl -u sc-console-agent -f                       # logs
sudo systemctl start sc-console-agent-update-scheduler.service  # check for an update now
sudo journalctl -u sc-console-agent-update-scheduler.service    # update logs
sc-console-agent -v                                          # installed version
```

Remove it with `sudo apt-get purge sc-console-agent`.

## Configuration

The agent works without configuration. The installer writes the metadata service address to
`/etc/strettchcloud/config/agent.yaml`; everything else has a default.

To override a setting, add it to `/etc/sc-console-agent/environment` and restart the service:

| Variable | Default | Meaning |
|---|---|---|
| `SC_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `SC_AGENT_CONFIG` | — | Path to a YAML file with settings from [`config.example.yaml`](config.example.yaml) |
| `SC_RESOURCE_ID` | from metadata | This machine's resource ID |

The agent listens on UDP `9999` (knock) and TCP `60500`; both can be changed in the YAML file
(`console.udp_knock_port`, `console.http_notify_port`).

## Verifying packages

- **APT packages** are signed. The trusted signing keys and their fingerprints are in
  [`packaging/keys/`](packaging/keys/).
- **Release `.deb` files** carry a build-provenance attestation:

  ```bash
  gh attestation verify sc-console-agent_<version>_amd64.deb --repo strettch-cloud/sc-agent
  ```

## Development

The toolchain is pinned in `mise.toml`. Install [mise](https://mise.jdx.dev), then:

```bash
mise install           # once
mise run build         # GOOS=linux GOARCH=arm64 mise run build to cross-compile
mise run test
mise run lint
mise run security-scan # Trivy + govulncheck
```

How CI builds and publishes releases is described in [.github/README.md](.github/README.md).

## Contributing and security

Contributions are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). Please report
vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in a public issue.

## License

[Apache License 2.0](LICENSE).
