# Install on Debian or Ubuntu

`install.sh` installs dyno as a systemd service on Debian, Ubuntu or any other
systemd Linux on amd64 or arm64: a VM, a Raspberry Pi, a container you made
yourself. For a Proxmox host, [`proxmox-lxc.md`](proxmox-lxc.md) does this
inside a new container for you.

## Install

```sh
curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/install.sh && sudo sh install.sh
```

The script downloads the binary for your machine from the latest release,
checks it against the release's `SHA256SUMS`, installs it to
`/usr/local/bin/dyno`, and starts a `dyno` service. It ends by printing the
address to open, for example `http://192.168.1.50:3966`. Open it and press
start.

Read the script before you run it if you like; it's short. Without curl, use
`wget https://github.com/redlinelabs-dev/dyno/releases/latest/download/install.sh`.

## Settings

The first install writes `/etc/dyno/dyno.env`:

| Variable | Default | What |
|---|---|---|
| `DYNO_LISTEN` | `:3966` | Address and port for the test page and API |
| `DYNO_METRICS_LISTEN` | off | Address for Prometheus `GET /metrics`, e.g. `127.0.0.1:3967` (ADR-0017) |

Edit it, then `sudo systemctl restart dyno`. To set them at install time:
`sudo DYNO_LISTEN=:8080 sh install.sh`.

## Upgrade

Run the same command again. It replaces the binary, keeps your settings and
history, and restarts the service. To install a particular version, set
`DYNO_VERSION=v0.1.0`.

## Where things are

| What | Where |
|---|---|
| Binary | `/usr/local/bin/dyno` |
| Service | `/etc/systemd/system/dyno.service` (replaced on upgrade) |
| Settings | `/etc/dyno/dyno.env` (kept on upgrade) |
| Test history | `/var/lib/dyno/dyno.db` |
| Logs | `journalctl -u dyno` (JSON, one event per line) |

The service runs as a throwaway system user (`DynamicUser`) with no
capabilities, a read-only view of the system, and only its data directory
writable. `systemd-analyze security dyno` rates it 1.4, "OK".

## Uninstall

```sh
sudo sh install.sh --uninstall   # keeps the test history and settings
sudo sh install.sh --purge       # deletes them too
```

## Before you expose it

dyno has no login (ADR-0008). Anyone who can reach the port can run tests,
which use your bandwidth, and read the history. Keep it on your LAN or a VPN
such as Tailscale, or put it behind a reverse proxy that does auth.

## Troubleshooting

- `systemd isn't running here`: the machine or container doesn't run systemd.
  Use the [Docker image](docker.md).
- The service fails with `status=226/NAMESPACE`: you're in an LXC container
  without nesting. On Proxmox, enable Options, Features, Nesting for the
  container and restart it.
