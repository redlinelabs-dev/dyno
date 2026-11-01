# Install on Proxmox VE (LXC)

`proxmox-lxc.sh` runs on the Proxmox host. It creates a small unprivileged
Debian container and installs dyno in it as a systemd service, the same way
[`install.sh`](debian.md) does on any Debian machine.

## Install

In the Proxmox host's shell (the web UI's node, Shell, or SSH as root):

```sh
curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/proxmox-lxc.sh && sh proxmox-lxc.sh
```

It downloads the dyno binary and the installer on the host and checks both
against the release's `SHA256SUMS`. It then finds or downloads the newest
Debian standard template and creates the container:

| Setting | Default | Change with |
|---|---|---|
| Container id | next free id | `DYNO_CTID=120` |
| Hostname | `dyno` | `DYNO_HOSTNAME=speedtest` |
| CPU, memory, disk | 1 core, 256 MiB, 2 GiB | `DYNO_CORES`, `DYNO_MEMORY`, `DYNO_DISK` |
| Root disk storage | first active storage that holds containers | `DYNO_STORAGE=local-zfs` |
| Network | `vmbr0`, DHCP | `DYNO_BRIDGE`, `DYNO_IP=192.168.1.50/24` with `DYNO_GW=192.168.1.1` |
| Unprivileged, nesting on, start at boot | always | |

It ends by printing the address to open, for example
`http://192.168.1.50:3966`. Open it and press start.

Put settings in front of the command, for example:

```sh
DYNO_CTID=120 DYNO_IP=192.168.1.50/24 DYNO_GW=192.168.1.1 sh proxmox-lxc.sh
```

`DYNO_LISTEN` and `DYNO_METRICS_LISTEN` are passed to the installer; see
[`debian.md`](debian.md#settings).

## With Tailscale

```sh
DYNO_TAILSCALE=1 sh proxmox-lxc.sh
```

This gives the container `/dev/net/tun`, installs Tailscale in it, and prints
the `tailscale up` command that joins your tailnet. Set `TS_AUTHKEY` to an
auth key to join without the login link.

## Upgrade

On the host, with the container's id:

```sh
DYNO_CTID=120 sh proxmox-lxc.sh --upgrade
```

Settings and history stay. Inside the container, `install.sh` works too,
as on any Debian machine.

## Where things are

Inside the container, as on [Debian](debian.md#where-things-are): settings in
`/etc/dyno/dyno.env`, history in `/var/lib/dyno/dyno.db`, logs in
`journalctl -u dyno`. Back up the whole container with Proxmox's own backups.

## Uninstall

`pct destroy <id>` on the host removes the container with its history. Stop it
first with `pct stop <id>`.

## Notes

- dyno measures the path to the container. A container on a bridge sees the
  host's NIC speed, so a 1 Gbit/s host tests up to 1 Gbit/s.
- One core is plenty for gigabit: dyno used about 5% CPU at 1 Gbit/s in the
  footprint bench. For multi-gigabit links, give it two.
