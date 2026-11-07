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
[`debian.md`](debian.md#settings). The script checks them before it creates the
container.

## Your own binary

`DYNO_BINARY` installs a dyno binary from the host instead of downloading one,
for a build of your own:

```sh
DYNO_BINARY=/root/dyno sh proxmox-lxc.sh
```

The script doesn't check that binary. An `install.sh` in the same folder as
`proxmox-lxc.sh` is used as is. Without one there, `install.sh` is downloaded
and checked against `SHA256SUMS`.

## With Tailscale

```sh
DYNO_TAILSCALE=1 sh proxmox-lxc.sh
```

This gives the container `/dev/net/tun`, then runs this inside it:

1. `apt-get install curl ca-certificates`.
2. Tailscale's own install script from `https://tailscale.com/install.sh`. It
   adds Tailscale's apt repository, and dyno doesn't checksum it.
3. `tailscale up`, if you set `TS_AUTHKEY` to an auth key. Without one, the
   script prints the `tailscale up` command for you to run, and you join your
   tailnet with the login link.

The key goes into the container through a file that is deleted afterwards,
never on a command line.

## Upgrade

On the host, with the container's id:

```sh
DYNO_CTID=120 sh proxmox-lxc.sh --upgrade
```

Settings and history stay. Inside the container, `install.sh` works too,
as on any Debian machine.

`--upgrade` only works on a container tagged `dyno`, which the script puts on
every container it creates, so a mistyped id can't install dyno into another
container. If it refuses a container that is dyno's, the tag was removed: add
`dyno` back to the container's tags in the Proxmox web UI.

## If it fails

If the script stops before it creates the container, it has changed nothing on
the host except, at most, a downloaded template. Fix the cause and run it
again.

If it fails after it creates the container, the script ends by printing this
(here for container 120):

```
dyno: container 120 was created and is still there; remove it with: pct stop 120; pct destroy 120
```

To look inside, run `pct enter 120`. To retry after fixing the cause, start the
container (`pct start 120`) and run `DYNO_CTID=120 sh proxmox-lxc.sh --upgrade`.
To start over, remove it with the two commands in that message.

`--upgrade` installs dyno only, not Tailscale. If a Tailscale step failed, dyno
is already running: finish the steps under "With Tailscale" inside the
container, or start over.

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
