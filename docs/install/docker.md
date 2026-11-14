# Install with Docker

dyno ships as one multi-arch image (linux/amd64 and linux/arm64) at
`ghcr.io/redlinelabs-dev/dyno`. It runs as a non-root user on an empty base
image and idles in about 20 MiB of memory.

## Compose

Save [`compose.yaml`](../../compose.yaml) on the host, then:

```sh
docker compose up -d
```

Open `http://<this-host>:3966` and press start.

## One command

```sh
docker run -d --name dyno --network host --restart unless-stopped -v dyno-data:/data ghcr.io/redlinelabs-dev/dyno:latest
```

## Why host networking

Port mapping (`-p 3966:3966`) puts Docker's NAT and, on some setups, its
userland proxy between the browser and dyno. Both cost throughput and add
latency, so the test would measure Docker as well as your network
(ADR-0003). With host networking dyno listens on the host's own port 3966.

Docker Desktop on macOS and Windows supports host networking from 4.34; turn
it on in Settings, Resources, Network. If you can't use host networking,
`-p 3966:3966` works, but treat results above a few hundred Mbit/s with
suspicion.

## Settings

Set these as `environment:` in `compose.yaml` or `-e` with `docker run`:

| Variable | Default | What |
|---|---|---|
| `DYNO_LISTEN` | `:3966` | Address and port for the test page and API |
| `DYNO_METRICS_LISTEN` | off | Address for Prometheus `GET /metrics`, e.g. `127.0.0.1:3967` (ADR-0017) |

## Upgrade

```sh
docker compose pull && docker compose up -d
```

Or with `docker run`: `docker pull ghcr.io/redlinelabs-dev/dyno:latest`, then
remove the container and run the command above again. Test history lives in
the volume, so it survives.

Pin a version instead of `latest` with a tag like `:0.1` (newest 0.1.x) or
`:0.1.0`.

## Where the data lives

Test history is one SQLite file, `/data/dyno.db`, in the `dyno-data` volume.
Compose and the `docker run` command above share that volume.

To back it up, stop dyno, copy the files out, and start it again. While dyno
runs, its newest results can sit in side files next to `dyno.db`, so a copy of
a running server can miss them. Stopping dyno writes them into the database.

```sh
docker compose stop        # or: docker stop dyno
docker run --rm -v dyno-data:/data -v "$PWD":/backup busybox sh -c 'cp /data/dyno.db* /backup/'
docker compose start       # or: docker start dyno
```

The `*` also takes the `dyno.db-wal` and `dyno.db-shm` files that dyno leaves
behind if it is killed instead of stopped.

dyno runs as user 65532, and `/data` in the image belongs to that user, which
is why the volume works as it is. A folder on the host doesn't: before you
mount one (`-v /srv/dyno:/data`), run `sudo chown 65532:65532 /srv/dyno`.

## Uninstall

```sh
docker compose down          # keeps the test history
docker compose down -v       # also deletes it
```

With `docker run`: `docker rm -f dyno`, and `docker volume rm dyno-data` to
delete the history.

## Before you expose it

dyno has no login (ADR-0008). Anyone who can reach port 3966 can run tests,
which use your bandwidth, and read the history. Keep it on your LAN or a VPN
such as Tailscale, or put it behind a reverse proxy that does auth.
