# Roadmap

Milestones run in order. Each ends on a check you can run, not on a feeling.
Before starting one, ask the owner the decisions-pending items listed under
"Needs".

## M0. Research and prototypes (done 2026-10-30)

Brief, three research docs, a runtime benchmark, two rounds of UI prototypes,
the combined pick (`prototypes/dyno-ui.html`) and ADRs 0001–0006.

## M1. Walking skeleton (built 2026-10-30; image build not yet run)

Needs: nothing open. Runtime and budgets: ADR-0006.

- A `server/` Go module serving `/dl`, `/ul`, `/ping`, `/api/v1/plan` and
  one embedded static page, following ADR-0002's hot-path rules.
- A root `Makefile` with `make check` (gofmt, go vet, go test -race, footprint check) and
  `make bench`.
- A footprint check, adapted from `bench/`, that fails when any ADR-0006 budget
  is exceeded.
- A `Dockerfile` (`FROM scratch`, non-root, `CGO_ENABLED=0`, amd64 and arm64) and a
  `compose.yaml` that starts dyno with no other files.

Done when `make check` passes here, the footprint check reports every
budget met, and the image builds on a machine with Docker. Building the image
is the owner's step until CI exists; give exact commands.

## M2. Web UI v1 (built 2026-10-30; phones, Safari, iperf3 and the image not yet checked)

Needs: nothing open (#9 is ADR-0008, #11 is ADR-0009). UI direction:
ADR-0004. Browser engine: ADR-0010.

- `web/` in TypeScript with no framework, built by Bun and embedded in the
  binary. It matches `prototypes/dyno-ui.html`, including the phone layout.
- Real measurement per ADR-0003: download, upload, idle and loaded latency,
  jitter, bufferbloat grade, and the client-CPU-limited label.
- The telemetry layer: latency detail, speed over time, setup, history.
- Results stored in SQLite and listed through the API. JSON copy works.
- Re-measure idle RSS once SQLite is in and make ADR-0006's one budget
  adjustment with the numbers (M1 left about 1 MiB of headroom; ADR-0007).

Done when a full test runs from Chrome and Safari on a phone and a desktop
against the container, the numbers agree with `iperf3` on the same LAN path
to within 5%, and the first screen shows only start, the number and latency.

Ordering below M2 comes from `docs/research/competitor-features.md` (2026-10-31),
whose §5 ranks the requests dyno doesn't meet yet. Signal numbers in brackets
are from there.

## M2.5. Operate it (`/metrics` and structured logs; built 2026-10-31, NAS scrape and dashboard live)

Needs: nothing open. Rules: ADR-0017 (nothing phones home; metrics on their
own listener).

Pulled forward from M3 because it's small, needs no owner call, and the owner
wants dyno in the homelab Prometheus and Grafana before it goes onto a VPS.
Prometheus is now table stakes: speedtest-tracker, henrywhitaker3 v2 and
MySpeed all ship it [~37].

- `GET /metrics` on `DYNO_METRICS_LISTEN` (off when unset), Prometheus text
  format written by hand, no client library. First set:
  - `dyno_build_info{version}`
  - `dyno_results_total{grade}` and histograms of stored results: download and
    upload bytes per second (Prometheus base units), idle and loaded latency seconds
  - `dyno_bytes_sent_total`, `dyno_bytes_received_total`,
    `dyno_streams_active`, `dyno_requests_total{route,code}`
  - `process_resident_memory_bytes`, `go_goroutines`
  - `go_gc_heap_live_bytes`, `go_memory_classes_total_bytes`,
    `go_memory_classes_heap_released_bytes`, added for the soak leak check
    (ADR-0006)
- Logs move from `log.Printf` to `log/slog` JSON on stdout. Conventions, adapted
  from the wide-event style (Sentry's "structure a log", Stripe's canonical log
  lines): stable `domain.action` event names (`server.start`, `result.store`),
  flat snake_case keys with units in the name, a low-cardinality `result`,
  warn for expected failures with `error.code`, one outcome event per stored
  result. Unlike that post, the outcome event carries `duration_ms`, because
  dyno has no tracing to hold it. No client IP in logs by default. Record the
  conventions in an ADR.
- `docs/api.md` documents `/metrics` and `DYNO_METRICS_LISTEN`.
- The homelab side, the owner's go-ahead first: a `dyno` scrape job in the
  NAS Prometheus (tailnet IP, since MagicDNS doesn't resolve on the NAS host)
  and a dashboard through its `spec.py` generator.

Done when `make check` passes with every footprint budget met, and the NAS
Prometheus scrapes this box's dyno and a Grafana panel shows its tests.

## M2.6. Install it (Docker and Proxmox LXC; scripts, CI and docs built 2026-10-31, CI green on GitHub 2026-11-14, not yet run on circuit)

Needs: nothing open. Rules: brief check #8, ADR-0021 (public once this works).

The owner wants dyno to be "dead simple for people to spin this up", with
"a robust solution for LXC's and docker containers" documented. This comes
before monitoring because nobody uses scheduled tests they can't install.

- Release builds: static binaries for linux/amd64 and linux/arm64, with
  checksums, from one `make release` target. Multi-arch image on GHCR with
  versioned tags. Both build in CI, since this box has no Docker.
- Docker: the shipped `compose.yaml` and a one-line `docker run`. Check the
  image here for the first time (open since M1): `make check`'s footprint
  run against the container, and brief check #1 (`docker compose up -d` with no
  other files gives a working test page).
- Any Debian or Ubuntu host: an install script that downloads the binary,
  verifies its checksum, adds a `dyno` systemd service (dynamic user, data in
  `/var/lib/dyno`, hardened unit) and handles upgrades by re-running. It's a
  POSIX shell script, an exception to the Bun-only rule, because users won't
  have Bun; record that in an ADR.
- Proxmox LXC: a script run on the Proxmox host, in the style of the
  community-scripts ProxmoxVE one-liners homelabbers already use. It creates
  a small unprivileged Debian container on the default bridge and runs the
  Debian install inside it. Optional Tailscale inside the container.
- Docs: one page per path (`docs/install/`), each ending in a working test page,
  plus upgrade, uninstall and where the data lives.
- First real use: the owner's dyno on circuit is installed with the LXC script.
  The NAS Prometheus job moves to it, and the `job!="dyno"` exclusion in the
  exporter-down alert comes out.

Done when a fresh circuit container and a fresh Docker host each go from one
documented command to a passing test, and the docs match what was run.

## M3. Monitoring

Scope: ADR-0019 (was #4). Testing an ISP needs a far end outside
the house: a dyno on a VPS, or one on a Pi at someone else's house over the
tailnet. The owner has one: `tailmeadow`, a tailnet node at a relative's house (about
500 Mbit/s), so M3 needs no VPS (ADR-0018). Tests there use someone else's
line, so default to the light test a few times a day. Speed tests are almost all egress,
so price by bandwidth: an hourly full test (10 s download) on a 0.5–1
Gbit/s line sends roughly 0.45–0.9 TB a month.

The top four requests are all here [scheduling and history ~97, alerts ~84,
an engine that isn't Ookla ~71, several vantage points ~45]. dyno can answer
the third outright, because it owns its engine: speedtest-tracker declined a
second engine in August 2026 and every other tracker wraps the Ookla CLI.

1. **Scheduler.** Scheduled tests run node to node: another dyno is the far
   end (ADR-0002). Several schedules, one per far end, so there is a series
   per vantage point. A "light" test with a byte cap for frequent runs. A
   failed run is stored as a row with its reason, not dropped, and the reason
   says whose end failed: the far end being down is not an ISP outage.
   Latency probes warm the path first, and a result far above the far
   end's latency baseline is flagged as a path change (a tailnet relay,
   a VPN), not an ISP slowdown (ADR-0019). Last run and
   next run shown, and schedules change without a restart (STT users call
   restart-to-change "error-prone").
2. **History.** One range picker for every chart (24 h, 7 d, 30 d, custom),
   not one per chart. A line per device, and a device filter (ADR-0022).
   Min, max, average and p10 per range. Failed runs marked on the chart.
   Retention from forever down to N days. CSV export. All behind the
   telemetry layer; the first screen doesn't change.
3. **Alerts that don't nag.** Nobody ships these rules yet:
   - thresholds absolute (Mbit/s, ms) or relative to a plan speed the owner
     enters ("below 80% of 500 Mbit/s")
   - fire after N breaches in a row, with an optional immediate retest before
     counting a breach
   - a recovery message, and a daily or weekly digest instead of per-test pings
   - "the scheduler has stopped" as its own alert (STT#1065's silent death,
     79 comments), also exposed as `dyno_schedule_last_success_timestamp_seconds`
   - channels: webhook and ntfy built in, plus an Apprise API URL for the rest,
     so channels aren't added one at a time. Pick between that and embedding
     Shoutrrr (26 channels, Netronome's choice) by binary size and RSS, and
     record it in an ADR.
4. **Live progress of a scheduled test** in the open UI [~11]. Ookla
   trackers can't show it; dyno's engine can.
5. The self-check warnings from ADR-0003 §8.

Done when two dynos run scheduled tests against each other for 48 hours with
failures injected, every failure shows on the chart, an N-in-a-row alert and
its recovery arrive through ntfy, and the footprint soak still passes.

## M4. Public release

Needs: M2.6. Licence and timing: ADR-0021 (AGPLv3 with a CLA, public once the
installers work).

- `LICENSE`, `CONTRIBUTING.md` and the CLA with its pull-request check, README for
  r/selfhosted. The README says plainly what dyno does that the others don't,
  from `competitor-features.md` §5: loaded latency and a bufferbloat grade, upload
  counted on the server, no correction factor, no login required, nothing sent
  to a third party.
- The name clearance check in ADR-0005.
- Fleet CI baseline (`JimmayVV/fleet-ci`), multi-arch images on GHCR with
  versioned tags (the image itself is built in M2.6).
- A footprint report from a real Raspberry Pi.
- Dashboard widgets [~27]: a Homepage widget PR upstream (it reads
  `/api/v1/results`, so dyno needs no new endpoint), and a documented Home
  Assistant REST sensor. Both are outward-facing, so the owner OKs each.

## M5. Stability and loss

Needs: #5 (packet loss).

- A stability mode behind the telemetry layer [~25]: continuous latency probes
  for a chosen time, with a live chart, min/avg/max, jitter and failed probes.
  LibreSpeed shipped one in July 2026. dyno's version reuses `/ping` and the
  chart from ADR-0012.
- Packet loss [~14] over WebTransport datagrams, per #5's recommendation.

## Later

- Auth beyond none [~30]: a trusted forward-auth header first, then OIDC.
  ADR-0008 already meets the bigger request, which is no login at all.
- Client IP and IPv4 against IPv6 [under 10], from the server's own view of
  the connection: no ipinfo.io-style lookup (ADR-0017).
- A CLI client [few votes, real use: librespeed-cli 853 stars,
  cloudflare-speed-cli 1,082].
- A light theme that follows the system [under 10]. Brand question first.
- A multi-node fleet view, an iOS app (#10), white-label and SSO for pro (#6).
- Not planned: InfluxDB and OpenTelemetry export. Prometheus covers the
  homelab case, and OTel's SDK costs more footprint than it returns here.
