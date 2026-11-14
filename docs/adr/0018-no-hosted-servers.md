# ADR-0018: Redline Labs runs no test servers

Status: accepted 2026-10-31 by the owner: "I don't want to build something I
need to pay to maintain just to gain clout."

## Decision

dyno is software people run themselves. Redline Labs runs no public test
servers, no server directory and no relay. Every far end a dyno tests against
is one its operator deployed or chose.

## Why

- A public far end costs money in proportion to its users. A speed test is
  almost all egress: a single 1 Gbit/s test sends about 1.25 GB. Ookla, M-Lab
  and Cloudflare carry this with ISP sponsors or a CDN's spare capacity.
  A solo project can't.
- An open endpoint that streams gigabytes on request attracts abuse. It needs
  uptime, rate limiting and an abuse contact, and it collects test takers' IP
  addresses, which brings privacy duties (ADR-0017).

## Consequences

- Scheduled ISP monitoring (decisions-pending #4) needs a far end outside the
  operator's network that the operator provides: a second dyno on a VPS or
  at another site. Pairing a second node has to be close to one command, or
  people with a single box won't bother.
- The owner tests the same way any operator would. Most M3 work is tested
  between two dynos on the LAN, with `tc netem` standing in for a bad ISP. The
  real internet path is checked on a VPS billed by the hour and deleted the
  same day, not one kept running.
- A public demo at M4 is a page that shows the UI, not an open test server.
  If one is ever run, it gets a byte cap and a rate limit, and the owner
  decides it separately.
