# Decisions pending

Owner calls that are still open. A build session asks about these and leaves
them undecided in code. When one is answered, record it in an ADR in
`docs/adr/` and delete it here. Each item carries a recommendation, so a short
answer is enough.

## 1. UI: which Tach variation

Round 1 (`prototypes/dyno-prototypes.html`,
https://claude.ai/artifact/Ujm3JRW6n5zTWGhquakAD8) offered Tach, Quiet and Dyno
sheet. On 2026-10-30 the owner chose Tach: "the tach is the best. ... Remember I
love fast.com the best, and I also love that there's telemetry options but that
it's hidden by default. I want to make sure that things like latency are still
accessible".

Round 2 (`prototypes/tach-variations.html`,
https://claude.ai/artifact/DknZQviR6fSNRP6q1BnYEQ) has four variations: **Fast
lane**, **Cluster**, **Race dash**, **Chrono**. Recommendation: Fast lane as the
first screen, with Cluster's latency gauge and its peak-latency marker as the
first telemetry tab.

## 2. Name

"dyno" is a working name: a dynamometer gives an honest power reading, which
fits Redline Labs. It needs a trademark and package-name check before going
public. Recommendation: keep it unless the check finds a conflict in
networking.

## 3. Confirm Rust for the server

ADR-0001 picks Rust (current-thread tokio) on measured footprint. Go was a
close second: about 3× the idle memory (11 MiB against 3.7 MiB), but simpler
to maintain. Recommendation: Rust, since the brief asks for the smallest
footprint possible.

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

## 6. Licence and the free/paid line

`docs/research/market-and-demand.md` §4 compares the options. FSL, BSL, the
Elastic License, PolyForm Shield and n8n's licence all leave a business's
internal use free, so none of them makes a business pay for a speed test.
Recommendation: AGPLv3 core with a contributor licence agreement, plus a
proprietary pro module unlocked by an offline Ed25519-signed licence file with
no phone-home, following Immich's verification pattern.

Still needed from the owner:

- **Who pays?** Candidates: MSPs placing probes at client sites, office IT, ISP
  support desks, or ISPs hosting public test servers.
- **What's in pro?** Candidates from the research: a fleet view over many
  sites, ISP SLA reports (measured against contracted speed), white-label
  branding, SAML/SCIM, roles and an audit log, and support. Keep OIDC and
  forward-auth free: they're the top homelab auth requests.

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

## 9. Exposure and auth

Recommendation: open on the LAN with no login by default. Optional built-in
password, trusted forward-auth header and OIDC, all free.

## 10. Native app

Which platform first, and when? Recommendation: iOS after the web UI ships,
since the Mac mini can build it. The API is designed for it either way
(ADR-0002).

## 11. Branding

Full Redline Labs dress (carbon, red, bone, Big Shoulders), or dyno's own
identity with a small "by Redline Labs" mark? All Tach variations so far use
full dress.
