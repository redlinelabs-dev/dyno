# ADR-0024: Install paths, release files, and POSIX sh installers

Status: accepted 2026-10-31. Implements brief check #8 and ROADMAP M2.6. The
owner: "dead simple for people to spin this up. A robust solution for LXC's
and docker containers should be documented."

## Decision

- **Three paths, one binary.** Docker (`compose.yaml` or one `docker run`),
  any systemd Debian or Ubuntu host (`install.sh`), and Proxmox LXC
  (`proxmox-lxc.sh`, run on the host, which creates a container and runs the
  same `install.sh` inside it). Each has a page in `docs/install/` that ends
  in a working test page. All three run the same static binary.
- **Release files.** `make release` builds `dyno-linux-amd64`,
  `dyno-linux-arm64`, copies `install.sh` and `proxmox-lxc.sh`, and writes
  `SHA256SUMS` over all four. A pushed `v*` tag makes CI attach them to a
  GitHub release and push the image to `ghcr.io/redlinelabs-dev/dyno` with
  tags `X.Y.Z`, `X.Y` and `latest`. Scripts download from
  `releases/latest/download/`, so they need no version lookup.
- **The installers are POSIX sh, not Bun.** A fresh host has `sh` and curl or
  wget, not Bun, and making people install a runtime to install a static
  binary would defeat the point. This is the one exception to the repo's
  Bun-only rule for scripts. CI runs `dash -n` and ShellCheck on them.
- **Checksums catch corruption, not a compromised release.** The script and
  `SHA256SUMS` come from the same release, so they prove the download is
  intact, nothing more. Signing (cosign or minisign) can come later if
  anyone asks.
- **The systemd unit is sandboxed.** `DynamicUser` (no permanent account),
  `StateDirectory=dyno` for `/var/lib/dyno`, no capabilities, a read-only
  system, private `/tmp` and devices, a `@system-service` syscall filter that
  returns EPERM rather than killing, and `GOMEMLIMIT=64MiB` as in the image.
  `systemd-analyze security` rates it 1.4. Settings live in
  `/etc/dyno/dyno.env`, which upgrades never touch; the unit is the script's
  and gets replaced.
- **Upgrade means running the same command again.** `install.sh` replaces
  the binary atomically and restarts; `proxmox-lxc.sh --upgrade` with the
  container id pushes a new binary in. `install.sh --uninstall` keeps data;
  `--purge` removes it.
- **The Proxmox script fetches on the host.** It downloads and checks the
  binary and installer on the host and pushes them into the container with
  `pct push`, so the container needs no curl and no route to GitHub to get
  dyno. `DYNO_BINARY` installs a local build instead, which is how the owner's
  install on circuit can happen before the repo is public. The container is
  unprivileged with `nesting=1`, which systemd's sandboxing needs inside LXC.
  Tailscale is opt-in (`DYNO_TAILSCALE=1`) and gets `/dev/net/tun` through
  two lines in the container config.
- **`compose.yaml` names the published image.** It no longer builds from
  source, so it works on its own (brief check #1). Developers build locally
  with `make image`. CI builds the image, tags it with the name compose uses,
  and runs `docker compose up -d --wait` from a folder holding only
  `compose.yaml`.
- **Not done:** a Docker Hub mirror, Helm, Unraid and TrueNAS templates,
  `.deb` packages, and an APT repository. Add them when users ask; the static
  binary keeps each one small.

## Consequences

- While the repo is private, none of the documented commands work for anyone
  without access: release downloads and GHCR images inherit the repo's
  visibility. ADR-0021 makes the repo public once these paths are tested,
  which also makes them work.
- The first tag push publishes the image. Ask the owner before pushing one.
- Verified on the dev box: `make release`; `install.sh` installing into a
  scratch root through install, upgrade, settings kept, a tampered binary
  refused, uninstall and purge; dyno serving tests under the unit's sandboxing
  as a user service; `proxmox-lxc.sh` against stand-in `pct`, `pveam`,
  `pvesh` and `pvesm`. Not verified here: `DynamicUser` under a real system
  manager, a real Proxmox host, the container build, and either workflow,
  which run only on GitHub.
