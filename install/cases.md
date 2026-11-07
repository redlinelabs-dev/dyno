# Installer test cases

What `install/install.test.ts` checks in `install.sh` and `proxmox-lxc.sh`. Each test name starts
with its case id. Spec, in order of authority: `docs/brief.md` check #8,
`docs/adr/0024-install-paths.md`, `docs/install/debian.md`, `docs/install/proxmox-lxc.md`, and the
scripts' header comments. Where the docs promise something, the case checks the promise.

Test hooks the scripts already have (public for tests only):
- install.sh: `DYNO_BASE_URL` replaces the GitHub release URL, `DYNO_ROOT` installs under a
  directory instead of `/` (and skips the root check, the systemd check and the health check),
  `SYSTEMCTL` replaces `systemctl`.
- proxmox-lxc.sh: `DYNO_BASE_URL` as above, `PVE_LXC_DIR` replaces `/etc/pve/lxc`.
- Anything else (`id`, `uname`, `curl`, `wget`, `pct`, `pveam`, `pvesh`, `pvesm`, `sleep`,
  `hostname`) is replaced with a stub on `PATH`.

## install.sh

| id | case | expected |
|---|---|---|
| I1 | fresh install from a release | binary at `<root>/usr/local/bin/dyno`, executable; unit at `<root>/etc/systemd/system/dyno.service`; settings at `<root>/etc/dyno/dyno.env` with `DYNO_LISTEN` and `DYNO_METRICS_LISTEN` commented out; systemctl called `daemon-reload`, then `enable … dyno`, then `restart dyno`, in that order; exit 0; prints `installed <version>` and an `http://…:3966` address |
| I2 | `DYNO_LISTEN=:8080 DYNO_METRICS_LISTEN=127.0.0.1:3967` on first install | both written uncommented into dyno.env; the printed address ends in `:8080` |
| I3 | re-run with the same binary | settings file byte-for-byte unchanged (even if `DYNO_LISTEN` now differs), says it keeps them; prints `reinstalled <version>` |
| I4 | re-run with a newer binary | binary replaced; prints `upgraded <old> to <new>`; settings unchanged; unit rewritten (a hand edit to the unit is gone) |
| I5 | checksum mismatch | non-zero exit, message says checksum mismatch; an existing installed binary is untouched; systemctl never called; nothing new under root |
| I6 | `SHA256SUMS` has no line for this arch | non-zero exit, says so; nothing installed |
| I7 | download fails (404 / curl error) | non-zero exit, `download failed`; nothing installed |
| I8 | default version | binary and SHA256SUMS fetched from `https://github.com/redlinelabs-dev/dyno/releases/latest/download/…` |
| I9 | `DYNO_VERSION=v0.1.0` | fetched from `…/releases/download/v0.1.0/…` |
| I10 | no curl, wget present | downloads with wget and installs |
| I11 | neither curl nor wget | non-zero exit; message names curl |
| I12 | `DYNO_BINARY=<file>` | installs that file, makes no download at all |
| I13 | `DYNO_BINARY` points at nothing | non-zero exit, says no file; nothing installed |
| I14 | binary that can't run (`-version` fails) | non-zero exit, says the binary doesn't run here; an existing installed binary is untouched; systemctl never called |
| I15 | `uname -m` = x86_64 / amd64 / aarch64 / arm64 / riscv64 | amd64 for the first two, arm64 for the next two (fetches `dyno-linux-<arch>`); riscv64 fails naming the machine type |
| I16 | `uname -s` not Linux | non-zero exit before anything is touched |
| I17 | not root and no `DYNO_ROOT` | non-zero exit saying to run as root, before anything is touched |
| I18 | after any run, success or failure | no `dyno.new` left beside the binary; the script's temp directory is gone |
| I19 | unit content | runs `/usr/local/bin/dyno`, `DynamicUser=yes`, `StateDirectory=dyno`, `Environment=DYNO_DATA=/var/lib/dyno`, `Environment=GOMEMLIMIT=64MiB`, `EnvironmentFile=-/etc/dyno/dyno.env`, `WantedBy=multi-user.target` |
| I20 | `systemctl restart` fails | non-zero exit |
| I21 | `--uninstall` | service disabled and stopped, unit and binary removed, daemon-reload; settings and data dirs kept; message says they were kept |
| I22 | `--purge` | as --uninstall, plus `<root>/var/lib/private/dyno`, `<root>/var/lib/dyno` and `<root>/etc/dyno` gone |
| I23 | `--uninstall` when never installed | exit 0, no error |
| I24 | unknown option | exit 1, message on stderr names the option; `--help` / `-h` exit 0 and print usage that mentions `--uninstall` and `--purge` |

## proxmox-lxc.sh

| id | case | expected |
|---|---|---|
| P1 | create with every default | id from `pvesh get /cluster/nextid`; root disk on the first *active* storage `pvesm status -content rootdir` lists; template is the newest `debian-13-standard_*` from `pveam available --section system` (version sort, not text sort), downloaded to the first active `vztmpl` storage only if `pveam list` lacks it; `pct create` with hostname `dyno`, 1 core, 256 MiB, 2 GiB, `--unprivileged 1`, `--features nesting=1`, `--onboot 1`, net0 `name=eth0,bridge=vmbr0,ip=dhcp`; then `pct start`; binary and install.sh pushed into the container, the binary with mode 0755; install.sh run inside with `DYNO_BINARY` set to the pushed binary; the push folder removed afterwards; prints the container's address with `:3966`, and the `--upgrade` command with the id |
| P2 | overrides | `DYNO_CTID`, `DYNO_HOSTNAME`, `DYNO_STORAGE`, `DYNO_BRIDGE`, `DYNO_CORES`, `DYNO_MEMORY`, `DYNO_DISK` reach `pct create`; `DYNO_IP=192.168.1.50/24 DYNO_GW=192.168.1.1` gives net0 `…,ip=192.168.1.50/24,gw=192.168.1.1` |
| P3 | static `DYNO_IP` without `DYNO_GW` | non-zero exit before any download or pct call |
| P4 | `DYNO_CTID` of an existing container | non-zero exit saying it exists and naming `--upgrade`; no `pct create` |
| P5 | `--upgrade` without `DYNO_CTID` | non-zero exit; no pct state change |
| P6 | `--upgrade` on a stopped container | non-zero exit saying it isn't running |
| P7 | `--upgrade` on a running container | no `pct create`, no `pct start`, no template work; pushes the binary and install.sh, runs install.sh, removes the push folder; no Tailscale steps even with `DYNO_TAILSCALE=1` |
| P8 | checksum mismatch on the binary, or on install.sh | non-zero exit, says checksum mismatch; no `pct create`, `push` or `exec` |
| P9 | `DYNO_BINARY=<file>` | that file is pushed; no binary download; install.sh beside the script is pushed |
| P10 | no Debian 13 template | newest `debian-12-standard_*` used; neither → non-zero exit naming Debian 12 or 13 |
| P11 | no active rootdir storage and no `DYNO_STORAGE` | non-zero exit naming DYNO_STORAGE |
| P12 | `DYNO_TAILSCALE=1` | the container's `<PVE_LXC_DIR>/<id>.conf` gains the `/dev/net/tun` allow and mount lines (once), Tailscale is installed inside; with `TS_AUTHKEY` it runs `tailscale up --auth-key=file:/root/ts-authkey --hostname <name>` (the key goes in as a file, R17); without, it prints the `tailscale up` command |
| P13 | the container never gets an address | non-zero exit after 30 tries naming DYNO_BRIDGE / DYNO_IP (stub `sleep`) |
| P14 | `DYNO_LISTEN=:8080` | passed to the install inside; printed address ends `:8080` |
| P15 | not root | non-zero exit; no pct calls |
| P16 | no `pct` / `pveam` on PATH | non-zero exit pointing at install.sh |
| P17 | unknown option | exit 1; `--help` exit 0 and mentions `--upgrade` |
| P18 | `uname -m` unsupported | non-zero exit before any pct call |

## Docs agree with the scripts (D)

| id | case | expected |
|---|---|---|
| D1 | every `DYNO_*` variable the docs' settings tables list | is read by the script it's documented for |
| D2 | every default the Proxmox doc's table states (1 core, 256 MiB, 2 GiB, vmbr0, DHCP, hostname dyno) | is what P1 observes |

## Added after black-box round 1 (now part of the spec)

| id | case | expected |
|---|---|---|
| R1 | proxmox-lxc.sh: SHA256SUMS lacks the line for the binary or for install.sh, or is empty | non-zero exit naming the file; nothing created, pushed or executed |
| R2 | either script run under any umask (077, 000) | the installed binary is executable by others; nothing the script creates is group- or world-writable |
| R3 | `DYNO_LISTEN` / `DYNO_METRICS_LISTEN` set to anything but a port (`:8080`) or host and port (`0.0.0.0:3966`, `[::1]:8080`, `dyno.lan:80`), using only letters, digits and `. : [ ] -` | refused before anything is touched (in proxmox-lxc.sh, before any pct call), naming the variable; an empty value counts as unset. install.sh never runs `dyno.env` as shell: it reads `DYNO_LISTEN` from it as text (last uncommented line, one pair of quotes stripped) |
| R4 | proxmox-lxc.sh: `pvesh get /cluster/nextid` fails or prints nothing, or `DYNO_CTID` isn't all digits | non-zero exit before any pct state change |
| R5 | proxmox-lxc.sh: the install inside the container fails | the pushed folder is still removed; non-zero exit with the install's own status; the message names the container, the install's status and the `--upgrade` command (`... failed (exit <n>); its message is above. Fix that, then run: DYNO_CTID=<id> sh proxmox-lxc.sh --upgrade`); in create mode the "still there" line of R15 follows |
| R6 | every download | curl has `--connect-timeout` and `--max-time`; wget has `-T` and `-t` |
| R7 | INT, TERM or HUP during a run | the temp directory is removed |
| R8 | the printed link | the first IPv4 address from `hostname -I`, else the first IPv6 in brackets, else the `<this-host>` placeholder |
| R9 | proxmox-lxc.sh with `DYNO_BINARY` and no install.sh beside the script | the downloaded install.sh is checked against the release's SHA256SUMS (a line for install.sh is required); mismatch or missing line: non-zero exit, nothing created or pushed. The local binary itself has no checksum, as the docs say |

## Added after review round 1 (now part of the spec)

R10 to R17 come from the clean-context review (black-box round 2 confirmed R10, R12 and R15 as B3, B8 and B10); R18 and R19 are black-box round 2 findings B5 and B6. The tests are named `R10 ...` to `R19 ...` in `install/install.test.ts`.

| id | case | expected |
|---|---|---|
| R10 | install.sh: writing the new binary fails (full disk, read-only `/usr/local`): the copy to `dyno.new`, its `chmod`, or the rename over the old binary. Under `set -e` a failing `cp` in `cp && mv` doesn't stop the script, so it went on to write the unit, restart and report "upgraded" while the old binary ran. A signal during the copy left `dyno.new` behind | non-zero exit; stderr says `couldn't write <bin>.new; nothing was changed` (`couldn't replace <bin>; nothing was changed` for the rename); the old binary, unit, settings and history are as they were; no `dyno.new`, no temp folder; systemctl never called; no "installed", "reinstalled" or "upgraded" line |
| R11 | install.sh on a host whose `/tmp` is mounted `noexec` (CIS-hardened): the new binary can't run from the temp folder, so the check said "the binary doesn't run on this machine" | the binary is copied to `dyno.new` beside the old one, made executable and tried there, then renamed over the old one. A binary that really can't run is still refused with the old message, the old binary untouched and `dyno.new` removed (I14) |
| R12 | the printed link when `DYNO_LISTEN` names a host (`127.0.0.1:3967`, `[::1]:8080`, `localhost:8080`, `dyno.lan:3966`): the link used the `hostname -I` address, where a service bound to one address doesn't answer. install.sh takes the setting from `dyno.env` on a re-run, proxmox-lxc.sh from its environment | the link uses the named host. `:8080`, `0.0.0.0:8080` and `[::]:3999` still use the first `hostname -I` address (install.sh) or the container's address (proxmox-lxc.sh) |
| R13 | install.sh re-run with `DYNO_LISTEN` or `DYNO_METRICS_LISTEN` set while `dyno.env` exists: the file is kept, so the value was ignored without a word | `dyno.env` is untouched and the output says both settings apply only on the first install, and to change them to edit `/etc/dyno/dyno.env` and run `systemctl restart dyno`. Unset and empty values (proxmox-lxc.sh passes empty strings) say nothing, and neither does a first install |
| R14 | `systemctl edit dyno` keeps its override in `/etc/systemd/system/dyno.service.d` (the way to bind a port below 1024); `--purge` left it behind | `--uninstall` keeps the folder, as it keeps the other settings. `--purge` removes it, also after an `--uninstall` already removed the unit, and touches no other service's drop-ins |
| R15 | proxmox-lxc.sh fails after `pct create` worked: `pct start`, either push, the install inside, a Tailscale step, no address after 30 tries, or INT, TERM or HUP. The container stays, and the message said so only for the install failing | the failure's own message, then as the last line on stderr `dyno: container <id> was created and is still there; remove it with: pct stop <id>; pct destroy <id>`. Not on success, not for a failure before the create (a failed `pct create`, no template, a checksum mismatch, a signal during a download), not in `--upgrade` mode |
| R16 | `--upgrade` with a mistyped `DYNO_CTID`: it pushed a binary into whatever running container had that id and ran a script there as root | it reads the `tags:` line of `pct config <id>` (tags split on `;`, `,` and spaces) and refuses unless one tag is exactly `dyno`, before any download, push or exec: `container <id> isn't tagged dyno; check DYNO_CTID. If it is dyno's container, add the tag dyno to it in the Proxmox UI and run this again`. `dyno`, `dyno;web`, `web;dyno` pass; `dynomite`, `mydyno;web`, an empty tags line and no tags line are refused. A container the script created passes, until its tag is removed |
| R17 | `TS_AUTHKEY` was an argument of `pct exec ... tailscale up --auth-key <key>`, so the host's process list showed it | the key is written to a file in the script's temp folder and pushed to `/root/ts-authkey` with `--perms 0600`; `tailscale up --auth-key=file:/root/ts-authkey --hostname <name>` runs; the file is deleted in the container whether or not `up` worked, and the run exits with `up`'s status. No `pct` argument holds the key, whatever characters it has |
| R18 | install.sh re-run over a `dyno.env` holding `DYNO_LISTEN = :7005` (systemd ignores blanks around `=`): the script read the default `:3966`, printed the wrong link and, on a real host, polled the wrong port and failed with "didn't answer within 10 s" while the service was fine | blanks (spaces or tabs) around `=` are skipped. `;` and `#` comment lines, a tab before the name, a quoted value with trailing blanks, CRLF, `MY_DYNO_LISTEN` and `DYNO_LISTEN_X` behave as before |
| R19 | `DYNO_LISTEN` or `DYNO_METRICS_LISTEN` made only of allowed characters but not an address dyno can bind (`:99999`, `:65536`, `:0`, `::1:8080`, `host:80:80`, `:80:80`, `[::1:8080`, `[::1]`, a 20-digit port): dyno refuses them at startup and the service restarts in a loop | both scripts accept only an empty value, `:port`, `host:port` (host: letters, digits, `.` and `-`) and `[v6]:port` (v6: hex digits, `:` and `.`, one pair of brackets), the port 1 to 65535 and at most five digits, so `test` never sees a number it can't hold and nothing wraps round to a small one. `:8080`, `127.0.0.1:3967`, `0.0.0.0:3966`, `[::1]:8080`, `[::]:3966`, `localhost:8080`, `dyno.lan:3966` stay accepted. A refusal is one stderr line naming the variable, before anything is touched (proxmox-lxc.sh: before any download or pct call) |
