# dyno

A self-hosted network speed test by Redline Labs. Press start, get one honest
number and your latency. Open telemetry when you want the rest: bufferbloat
grade, latency under load, speed over time, scheduled tests and history.

It's designed to idle in under 24 MiB of memory and ship as one static binary
in one container.

Status: M2. The web UI runs real tests and keeps a history; scheduled tests
and monitoring come in M3. See `docs/ROADMAP.md`.

## Running it

| Where | Command | Guide |
|---|---|---|
| Docker | `docker compose up -d` with [`compose.yaml`](compose.yaml) | [docs/install/docker.md](docs/install/docker.md) |
| Debian, Ubuntu, Raspberry Pi | `curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/install.sh && sudo sh install.sh` | [docs/install/debian.md](docs/install/debian.md) |
| Proxmox VE (new LXC container) | `curl -fsSLO https://github.com/redlinelabs-dev/dyno/releases/latest/download/proxmox-lxc.sh && sh proxmox-lxc.sh` on the host | [docs/install/proxmox-lxc.md](docs/install/proxmox-lxc.md) |

Then open `http://<this-host>:3966` and press start. Each guide covers
settings, upgrades, uninstalling and where the test history lives.

From source: `make build && bin/dyno` (needs Go and Bun). The API is in
`docs/api.md`.

There is no login (ADR-0008). Anyone who can reach dyno can run tests and
read the history, so keep it on your LAN or put it behind a VPN or a reverse
proxy that does auth.

## Where things are

| Path | What |
|---|---|
| `docs/brief.md` | The original request and the definition of done |
| `docs/decisions-pending.md` | Open questions for the owner |
| `docs/adr/` | Settled decisions: runtime, architecture, measurement |
| `docs/research/` | Market and demand, measurement methodology, runtime benchmark |
| `prototypes/` | Interactive UI prototypes; open the HTML files in a browser |
| `server/` | The Go server |
| `web/` | The web UI, TypeScript built by Bun and embedded in the server |
| `scripts/footprint.ts` | The footprint check behind `make check` and `make bench` |
| `bench/` | Reproducible runtime benchmark (Rust, Go, Zig, Bun) |

## Starting a build session

Open Claude Code in this folder and paste:

> Read CLAUDE.md, then docs/brief.md, docs/decisions-pending.md and
> docs/ROADMAP.md. Ask me any pending decision that blocks the next
> milestone. Then plan that milestone and build it.

## License

dyno is licensed under the GNU Affero General Public License v3 (`LICENSE`).
You can run, change and share it. If you run a changed version as a network
service, you have to publish your changes. Contributions: see
`CONTRIBUTING.md`.
