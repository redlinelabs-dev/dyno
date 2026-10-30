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

## M3. Monitoring

Needs: #4 (scope of scheduled tests).

- Scheduled node-to-node tests (a second dyno as the far end), history charts
  with min, max and average, and configurable retention.
- `/metrics` for Prometheus, and alerts through webhook, ntfy or Apprise.
- The self-check warnings from ADR-0003 §8.

## M4. Public release

Needs: #6 (licence), #7 (visibility).

- Licence files, contributor licence agreement if #6 keeps it, README for
  r/selfhosted.
- The name clearance check in ADR-0005.
- Fleet CI baseline (`JimmayVV/fleet-ci`), multi-arch images on GHCR.
- A footprint report from a real Raspberry Pi.

## Later

Packet loss over WebTransport (#5), a multi-node fleet view, an iOS app (#10),
a CLI client, white-label and SSO for pro (#6).
