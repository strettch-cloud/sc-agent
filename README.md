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
        │ 1. signed "knock" (UDP 60124) ───────────►  sc-console-agent
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
  this machine's resource ID and a timestamp no older than `signature_timeout` (30 s by default).
  Forged packets and captures older than that are rejected; a knock replayed within the window
  only makes the agent ask the platform for the session key again.
- **Credentials:** the agent holds no secrets on disk. It gets a short-lived token, the
  platform's public key and the API address from the link-local metadata service.
- **Short-lived access:** console keys are removed from `authorized_keys` every
  `key_cleanup_interval` (2 minutes by default), so a console key can only be used to start a
  session for a short time; sessions already open are not interrupted.
- **Heartbeat:** the agent reports its version to the platform every 5 minutes.

## Installation

On an Ubuntu VM, as root:

```bash
curl -sSL https://repo.cloud.strettch.com/console/install.sh | sudo bash
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

The agent's settings are fixed: its ports, timeouts, retries and how long a console key stays
are the same on every VM, so a mistaken edit can't break the console or make many agents
overload the platform. The installer writes the metadata service address to
`/etc/strettchcloud/config/agent.yaml`, and the resource ID comes from
`dmidecode -s system-uuid`. The agent listens on UDP `60124` (knock) and TCP `60125`.

The log level is the only setting you can change. Set `log_level` in
`/etc/sc-console-agent/config.yaml` (see [`config.example.yaml`](config.example.yaml)), or
`SC_LOG_LEVEL` in `/etc/sc-console-agent/environment`, then restart the service. The variable wins
over the file.

| Setting | Values | Notes |
|---|---|---|
| `log_level` in config.yaml, or `SC_LOG_LEVEL` | `debug`, `info` (default), `warn`, `error` | A config.yaml that sets anything else, or a level the agent doesn't know, is ignored as a whole: the agent logs why and runs on its defaults. `sc-console-agent -validate-config` reports the problem. |
| `SC_RESOURCE_ID` | this machine's resource ID | Used only when `dmidecode` finds no ID. Where it finds one, `SC_RESOURCE_ID` is ignored. |

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
mise run test          # unit tests, then the real agent in Docker against fake servers
mise run test-e2e      # only the Docker tests
mise run lint
mise run security-scan # Trivy + govulncheck
```

The end-to-end tests build the agent with the `e2e` tag, which adds `SC_TEST_*` variables for fake
servers, free ports and short intervals (`pkg/config/overrides_e2e.go`); release builds don't have
them. How CI builds and publishes releases is described in [.github/README.md](.github/README.md).

## Contributing and security

Contributions are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). Please report
vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in a public issue.

## License

[Apache License 2.0](LICENSE).
