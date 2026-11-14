# ADR-0029: Schedules in the API

Status: accepted 2026-11-13 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Fills in how ADR-0028's schedules are set and
timed. The contract is `docs/api.md`, "Schedules".

## Decision

- **A far end is typed the way people say it.** `tailmeadow` and
  `tailmeadow:3966` mean `http://tailmeadow:3966`: no scheme means http, and
  then no port means dyno's 3966. With a scheme, the scheme's own port
  applies. Only http and https, with no path, query, fragment or user info.
  The stored form is the base URL.
- **Times are a grid in UTC.** A schedule runs every N minutes, with N from a
  menu whose every entry divides a day (15, 20, 30, 60, 120, 180, 240, 360,
  480, 720, 1440). It runs at midnight UTC plus an offset of M minutes, plus
  multiples of N, where 0 ≤ M < N. The times are the same every day and never
  drift. The UI turns local times into offsets.
- **Missed runs aren't made up.** After downtime a schedule waits for its next
  time, so a restart doesn't set off a burst of tests.
- **A run gives way to this dyno's own test traffic.** Before a run starts,
  if this dyno is serving `/dl` or `/ul`, the run waits up to 2 minutes for
  that to end, then runs anyway. Two dynos that test each other then
  don't overlap.
- **A run gets at most 5 minutes**, then it is stopped and stored as
  `timeout`.
- **Only a new far end is checked.** Adding a schedule, or changing its far
  end, reads the far end's plan first (10 s). Pausing, renaming or retiming
  doesn't, so a schedule can be paused while its far end is down.
  `POST /api/v1/schedules/check` runs the same check on its own, for the add
  flow's "Checking..." step. At most 4 checks run at once.
- **Deleting a schedule keeps its results.** A run in progress is stopped and
  nothing is stored for it. Results record the far end they ran against, so a
  deleted schedule's line can still be labelled. Schedule ids are never
  reused, so a new schedule never inherits an old one's results.
- **Run now** queues a run, even of a paused schedule. A run already queued or
  running isn't queued twice. The API refuses it for 60 s after the
  schedule's last result, so a client stuck in a loop can't hammer a far end.
- **The path-change baseline** (ADR-0028) counts only results against the
  far end's current address. Pointing a schedule somewhere else starts a
  fresh baseline.
- **The scheduler never dies quietly.** A run that panics or fails inside
  dyno is stored as a failure with code `internal`, and the next run happens
  on time. STT#1065's silent stop, 79 comments long, is the failure this
  guards against.
- **Metrics per schedule**, labelled by schedule id and name:
  - `dyno_schedule_last_success_timestamp_seconds`;
  - `dyno_schedule_runs_total{result}`;
  - gauges of the last good result's speeds and latencies.

  Scheduled results stay out of `dyno_results_total` and the
  `dyno_result_*` histograms, which keep meaning client results. Existing
  dashboards don't change.

Amended 2026-11-14, from building the schedules API:

- **A host name is ASCII:** letters, digits, `-`, `_` and `.`, in labels of
  up to 63 characters. A name in another script goes in as punycode
  (`xn--…`). Turning Unicode names into punycode needs IDNA tables the
  standard library doesn't have, and the stored form must be the address
  dyno dials. An IPv6 address goes in brackets, without a zone.
- **A check that fails on this server's side is a `500`** with
  `check_failed`, not a `422`. A `422` says the far end failed the check;
  this says dyno did.
- **A `PATCH` changes only the fields it sends**, even when another change
  lands during its far-end check. After the check, the API merges the patch
  with the schedule as stored again, and saves it under one lock.
- **"Serving `/dl` or `/ul`" lasts 5 s past the last stream.** A test has a
  gap between its download and its upload, and a scheduled run that started
  in it would overlap the upload. The server's metrics already count the
  open streams, so they answer the question.

## Why

- Tracker users type a hostname, not a URL. 3966 is dyno's port, so a bare
  host should find a dyno.
- A UTC grid needs no time zone data in a `FROM scratch` image (ADR-0006),
  and its times can't drift with how long runs take. The cost: a daily run
  set for 03:00 local moves an hour against local time when the clocks change
  for daylight saving.
- Making up missed runs fills the history with tests that all ran the moment
  dyno came back, which say nothing about the times they stand in for.

## Consequences

- Anyone who can reach a dyno can add schedules to it, and so make it send up
  to 100 MiB each way per light run to any dyno they name: 16 schedules
  every 15 minutes is about 300 GiB a day. The far-end check also tells them
  whether a dyno answers at an address this dyno can reach. On a home network
  that reaches nothing they couldn't reach themselves (ADR-0008). A dyno on
  the public internet, such as a far end on a VPS (ADR-0018), needs a guard
  on its write endpoints before it is safe. That is now a prerequisite of
  M4.
- Scheduled tests and browser tests of the same dyno can still overlap when
  a browser starts a test while a scheduled run is going. The browser's
  result isn't marked.
