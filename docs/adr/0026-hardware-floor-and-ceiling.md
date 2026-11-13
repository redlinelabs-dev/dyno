# ADR-0026: The floor is a Raspberry Pi 4 with 1 GB; the ceiling is 10 GbE

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Was decisions-pending #8.

## Decision

- **Floor:** dyno runs well on a Raspberry Pi 4 (arm64, 1 GB of RAM), and in
  the LXC the Proxmox script makes by default (1 core, 256 MiB of RAM,
  256 MiB of swap). Every footprint budget
  in ADR-0006 must hold there.
- **Ceiling:** a 10 GbE LAN. Browsers top out near 2.5 Gbit/s in Chromium and
  about 1 Gbit/s elsewhere (ADR-0003 §7), so 10 Gbit/s is measured node to
  node (scheduled tests, ADR-0028) or by a future native client.
- **The boxes it has to run on** are the owner's: circuit (Proxmox, the LXC
  path), the TrueNAS host (Docker), and a Pi for the M4 footprint report.

## Why

- The Pi 4 is the cheapest box a homelabber is likely to give dyno a home on.
  It sets the memory budget's meaning: 1 GB shared with the OS and other
  services.
- Promising 10 Gbit/s in a browser would be a promise browsers break. Node to
  node, the Go engine has no such limit.

## Consequences

- M4's footprint report comes from a real Pi 4. Until then, arm64 is
  cross-built and unrun (`docs/user-stories.md` story 4).
- A result above about 2.5 Gbit/s from a browser is suspect, and the
  client-limited label (ADR-0003 §7) should say so.
