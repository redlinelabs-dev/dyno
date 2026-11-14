# User stories

What people get from dyno, in their words, and where each story stands. The owner
owns this file: add, cut, reword and reorder freely. Build sessions turn the
stories into decisions (ADRs) and code, and keep the status column true.

Sources: the brief (`docs/brief.md`, which outranks this file), the owner's UI
decisions (ADRs 0004, 0011 to 0016, 0022), the roadmap, and what users of other
trackers ask for (`docs/research/competitor-features.md` §5, signal in
brackets). Drafted 2026-11-13 from those; not yet reviewed by the owner.

Status words: **shipped** (in the app and checked), **built** (done, waiting on
a check named beside it), **building**, **next**, **later**, and **your call**
(waiting on an owner decision, named beside it).

## Waiting on you

1. **Where monitoring lives** (stories 23 to 29). Three prototypes, built on
   the same 30 days of data: inside Telemetry, a Monitor tab, or a history page
   of its own. Pick one, or parts of several.

## Install it

| # | Story | Status |
|---|---|---|
| 1 | As a homelabber, I want to start dyno with `docker compose up -d` and no other files, so I can try it in a minute. | built: CI runs the shipped compose file; ships with v0.1.0 |
| 2 | As a Proxmox user, I want one command on the host that makes a small container running dyno, so I don't hand-build an LXC. | built: never run on a real Proxmox host (circuit) |
| 3 | As a Debian or Ubuntu user, I want a script that installs dyno as a locked-down service and upgrades it when I run it again. | built: CI runs it under real systemd |
| 4 | As a homelabber, I want dyno to sit near zero memory and CPU when idle, so it can live on a Pi or the smallest LXC. | shipped: budgets checked on every change (ADR-0006) |

## Test my connection now

| # | Story | Status |
|---|---|---|
| 5 | As anyone on the network, I want to open dyno and get my speed without a click, the way fast.com works. | shipped (ADR-0004) |
| 6 | As anyone, I want latency, idle and while busy, right under the number, because a fast line that lags is still a bad line. | shipped (ADR-0011) |
| 7 | As anyone, I want to stop a test that started on its own. | shipped (ADR-0014) |
| 8 | As a phone or desktop user, I want the test to fill my screen without scrolling. | shipped (ADR-0016) |
| 9 | As a keyboard or screen-reader user, I want to run a test and read every result. | shipped (ADR-0015); real screen readers not yet tried |
| 10 | As a phone user, I want it to work in mobile Safari and Chrome on Android. | built: not yet checked on real phones (M2's check) |

## Understand the result

| # | Story | Status |
|---|---|---|
| 11 | As a curious user, I want to see why the result is what it is: speed over time, latency under load, a bufferbloat grade. | shipped (ADRs 0012, 0013) |
| 12 | As a sceptic, I want to trust the number: upload counted by the server, no correction factor, and a label when my own device held the result back. | shipped (ADR-0003) |
| 13 | As a sceptic, I want a warning when something between me and dyno (a proxy, a CDN, compression) could make the number wrong. | next (M3, ADR-0003 §8) |
| 14 | As anyone, I want to copy a result as JSON. | shipped |

## Compare devices

| # | Story | Status |
|---|---|---|
| 15 | As a household, we want one shared history where every test says which device ran it, so we can compare the wired PC with the phone on Wi-Fi. | shipped (ADR-0022) |
| 16 | As a user, I want to name my device ("Kitchen iPad"). | shipped (ADR-0022) |

## Track my internet over time [~97, ~71, ~45]

| # | Story | Status |
|---|---|---|
| 17 | As a homelabber, I want dyno to test my internet on a schedule against another dyno I run (a VPS, a relative's house), so I have a record of what my ISP delivers. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 18 | As a homelabber, I want a light test for frequent runs, so a far end on a metered line isn't drained. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 19 | As a homelabber with more than one far end, I want each one as its own line, so one bad path doesn't hide in an average. | next |
| 20 | As a homelabber, I want a failed scheduled test kept with its reason, and the reason saying which end failed, because a far end being down is not an ISP outage. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 21 | As a homelabber, I want a test that ran over a slower path (a tailnet relay) flagged, not counted as my ISP being slow. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 22 | As a homelabber, I want to see when each schedule last ran and runs next, and to change schedules without a restart. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 23 | As a homelabber, I want one range picker (24 h, 7 d, 30 d, custom) for every chart, not one per chart. | your call: layout |
| 24 | As a homelabber, I want min, max, average and p10 for the range, readable without hovering. | your call: layout |
| 25 | As a homelabber, I want failed runs marked on the chart, not hidden. | your call: layout |
| 26 | As a homelabber, I want to watch a scheduled test live when the page is open. | your call: layout; the server side is next |
| 27 | As a homelabber, I want to export results as CSV. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 28 | As a homelabber, I want results kept forever by default, or for a number of days I choose. | built: server side and API; waiting on the monitoring UI (prototype pick, above) and a run against a real far end |
| 29 | As a household, we want our own browser tests on the same chart when we ask for them, one line per device. | your call: layout |

## Get told when it matters [~84]

| # | Story | Status |
|---|---|---|
| 30 | As a homelabber, I want an alert when speed or latency crosses a line I set (in Mbit/s, ms or % of my plan) a number of times in a row, with a retest first, so one bad test doesn't wake me. | next |
| 31 | As a homelabber, I want a message when it recovers, and a daily or weekly digest instead of a ping per test. | next |
| 32 | As a homelabber, I want an alert when the scheduler itself stops, because a tracker that dies silently is worse than none. | next |
| 33 | As a homelabber, I want alerts through ntfy, a webhook, or anything Apprise reaches. | next |

## Run it alongside everything else

| # | Story | Status |
|---|---|---|
| 34 | As a Prometheus and Grafana user, I want `/metrics`, so dyno sits on my existing dashboards. | shipped (M2.5) |
| 35 | As an operator, I want logs I can search. | shipped (ADR-0020) |
| 36 | As a Homepage or Home Assistant user, I want a widget with my latest result. | later (M4): the upstream pull requests need your OK |

## Stability and loss [~25, ~14]

| # | Story | Status |
|---|---|---|
| 37 | As a gamer or someone on video calls, I want a stability mode: latency for as long as I choose, with a live chart, jitter and dropped probes. | later (M5) |
| 38 | As the same person, I want packet loss. | later (M5, over WebTransport) |

## Later

| # | Story | Status |
|---|---|---|
| 39 | As a curious user, I want to see my IP address and whether I tested over IPv4 or IPv6, from dyno's own view. | later |
| 40 | As a scripter, I want a command-line client. | later |
| 41 | As someone with a light-themed system, I want dyno to follow it. | later; the look is your call (ADR-0004 made dyno dark only) |
| 42 | As a phone user, I want a native app that talks to my dyno. | later: iOS first, once the web UI ships |
| 43 | As someone exposing dyno beyond the LAN, I want a login in front of it: a trusted proxy header first, then OIDC. | later (ADR-0008 keeps the default login-free); a dyno facing the internet needs a guard on writes first, an M4 prerequisite (ADR-0029) |
