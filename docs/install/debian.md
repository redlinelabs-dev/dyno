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
`sudo DYNO_LISTEN=:8080 sh install.sh`. Set this way, a value must be a port
(`:8080`) or a host and port (`127.0.0.1:3967`, `[::1]:8080`); the script
refuses anything else rather than write it into the file.

The variables on the install command apply to the first install only. Once
`/etc/dyno/dyno.env` exists, the installer keeps it, and says so if you pass
either variable again. To change a setting later, edit the file and restart.

### Ports below 1024

The service has no capabilities, so it can't bind port 80 or 443. With
`DYNO_LISTEN=:80`, dyno exits at start and systemd keeps restarting it. To give
it that one capability, add a drop-in. The installer rewrites only
`dyno.service`, so a drop-in survives upgrades:

```sh
sudo systemctl edit dyno
```

In the editor, add these lines and save:

```ini
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
```

Then set `DYNO_LISTEN=:80` in `/etc/dyno/dyno.env` and run
`sudo systemctl restart dyno`. Or leave dyno on its own port and put a reverse
proxy in front of it.

## Upgrade

Run the same command again. It replaces the binary, keeps your settings and
history, and restarts the service. To install a particular version, set
`DYNO_VERSION=v0.1.0`.

## Where things are

| What | Where |
|---|---|
| Binary | `/usr/local/bin/dyno` |
| Service | `/etc/systemd/system/dyno.service` (replaced on upgrade) |
| Your changes to the service | `/etc/systemd/system/dyno.service.d/`, from `systemctl edit` (kept on upgrade) |
| Settings | `/etc/dyno/dyno.env` (kept on upgrade) |
| Test history | `/var/lib/dyno/dyno.db` |
| Logs | `journalctl -u dyno` (JSON, one event per line) |

The service runs as a throwaway system user (`DynamicUser`) with no
capabilities, a read-only view of the system, and only its data directory
writable. `systemd-analyze security dyno` rates it 1.4, "OK".

## Back up

Stop the service, copy the history, and start it again. While dyno runs, its
newest results can sit in side files next to `dyno.db`, so a copy of a running
server can miss them. Stopping dyno writes them into the database.

```sh
sudo systemctl stop dyno
sudo sh -c 'cp /var/lib/dyno/dyno.db* "$PWD"'
sudo systemctl start dyno
```

Only root can open `/var/lib/dyno`, so root has to expand the `*` as well; that
is why `cp` runs inside `sh -c`. The copies land in the current folder and
belong to root. `sudo chown "$USER" dyno.db*` hands them to you. The `*` also
takes the `dyno.db-wal` and `dyno.db-shm` files that dyno leaves behind if it is
killed instead of stopped.

## Uninstall

```sh
sudo sh install.sh --uninstall   # keeps the test history and settings
sudo sh install.sh --purge       # deletes them too
```

`--uninstall` also keeps a drop-in you made with `systemctl edit`. `--purge`
deletes its folder, `/etc/systemd/system/dyno.service.d`, along with the rest.

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
- The install ends with `dyno didn't answer within 10 s`: the service started
  and then stopped again. `journalctl -u dyno -n 20` says why. Two reasons are
  common:
  - `bind: address already in use`: another service has the port. Set a free
    one in `/etc/dyno/dyno.env`, for example `DYNO_LISTEN=:8080`, and run
    `sudo systemctl restart dyno`.
  - `bind: permission denied` with a port below 1024: see
    [Ports below 1024](#ports-below-1024).
