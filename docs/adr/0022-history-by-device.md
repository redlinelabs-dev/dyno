# ADR-0022: One shared history, shown by device

Status: accepted 2026-10-31. The owner asked "are we mixing results between
devices in one shared history? What's the virtue in that if we are?" and
picked this option. The demo history at the time mixed Safari on iPhone
(around 450 Mbps over Wi-Fi) with Edge on Windows (around 885 Mbps wired), and
the phone layout hid the From column, so the mix looked like a flaky network.

## Decision

- **The server keeps one history** for every device that tests against it.
  That's what makes dyno a tracker and not just a test page: it is where you
  compare the wired PC with the phone on Wi-Fi, and where scheduled tests (M3)
  will land. A per-browser history would also vanish with the browser's
  storage.
- **History opens on "This device"**, with a switch to "All devices". All
  devices names each row's device: a From column on wide screens, a line under
  the time on a phone.
- **A device is a random id the browser makes for itself**, 16 random bytes
  in base64url, kept in localStorage as `dyno.device` and sent as `device`
  with every result. `GET /api/v1/results?device=<id>` lists one device's
  results. The id is not a login and not a secret, and it never leaves the
  owner's server: it isn't logged (ADR-0020) or exported as a metric label.
- **A device's name is the existing `client` label.** Setup has a "This
  device's name" field; left empty, the name is the browser label ("Safari on
  iPhone"). Two phones of the same model can then be told apart.

## Consequences

- Results saved before this change have no device, so they appear only under
  All devices.
- Clearing site data gives a browser a new id. Its older results stay under
  All devices with the name they were saved with.
- Renaming a device changes only the name on new results. Showing the latest
  name on every result of a device would need a devices table; not worth it
  until someone asks.
- History charts (M3) should draw a line per device rather than one line
  through a mix of devices.
- The schema gained a `device` column and a `(device, id)` index (migration 2).
