# Decisions pending

Owner calls that are still open. A build session asks about these and leaves
them undecided in code. When one is answered, record it in an ADR in
`docs/adr/` and delete it here. Each item carries a recommendation, so a short
answer is enough. Item numbers are stable; answered items are gone from
this list, not renumbered. Answered so far: #1 (ADR-0004), #2 (ADR-0005),
#3 (ADR-0006), #9 (ADR-0008), #11 (ADR-0009).

## 4. What v1 tests

- **A. Browser to this server**: Wi-Fi, LAN, VPN, or a VPS over the internet.
  This is what LibreSpeed and OpenSpeedTest do.
- **B. Server to the internet on a schedule**: ISP monitoring, which is what
  speedtest-tracker and MySpeed do by wrapping the Ookla CLI. Ookla's EULA
  allows only personal, non-commercial use on one computer, so dyno can't use
  it. The far end has to be another dyno node, for example on a cheap VPS.

Recommendation: A first. B is milestone 3, node to node, with history and
graphs designed for it from the start.

## 5. Packet loss in v1?

The research disagrees. The market research puts jitter and packet loss in
the MVP (via WebRTC, as Cloudflare does). The methodology research says leave
loss out of v1: WebRTC means ICE, DTLS and SCTP on the server, which is
expensive in code and footprint, and WebTransport datagrams are the lighter
route later. Recommendation: jitter in v1, packet loss in v1.1 over
WebTransport.

## 6. Licence

Partly answered 2026-10-30. The owner: "I don't _need_ to have this generate
revenue I just want to leave the option open to." So who pays and what goes in
pro can wait until someone asks to pay. Until then, anything that could become
paid stays in its own module (CLAUDE.md), and nobody builds pro features.

Still open, needed before the repo goes public (M4): the licence.
`docs/research/market-and-demand.md` §4 compares the options.

- **AGPLv3 with a contributor licence agreement** (recommended). Anyone can
  self-host and modify it; a company that runs a changed version as a service
  must publish its changes. The CLA keeps the owner able to sell a commercial
  licence or a pro module later, which is what "leave the option open" needs.
  The cost is that a CLA puts off some contributors.
- **MIT or Apache-2.0.** The most adoption and no CLA needed, and a separate
  proprietary pro module stays possible. But anyone, including a competitor,
  can ship dyno inside a paid product without giving anything back.

FSL, BSL, the Elastic License and PolyForm Shield all leave a business's
internal use free, so they protect against competing hosted services, not
unpaid business use.

## 7. Public from day one?

The repo is private. A private repo on the free org plan can't have rulesets,
so the fleet auto-merge baseline can't arm. Going public also builds an
audience on r/selfhosted early. Recommendation: go public when the first
working container exists, and adopt the fleet baseline then.

## 8. Hardware floor and ceiling

Recommendation: the floor is a Raspberry Pi 4 (arm64, 1 GB) and the ceiling is
a 10 GbE LAN; browsers top out near 2.5 Gb/s, so 10 Gb/s needs a native client
or a node-to-node test. Confirm, or name the boxes it has to run on (the TrueNAS
host, circuit, a Pi?).

## 10. Native app

Which platform first, and when? Recommendation: iOS after the web UI ships,
since the Mac mini can build it. The API is designed for it either way
(ADR-0002).

