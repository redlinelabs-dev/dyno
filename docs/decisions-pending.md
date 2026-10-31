# Decisions pending

Owner calls that are still open. A build session asks about these and leaves
them undecided in code. When one is answered, record it in an ADR in
`docs/adr/` and delete it here. Each item carries a recommendation, so a short
answer is enough. Item numbers are stable; answered items are gone from
this list, not renumbered. Answered so far: #1 (ADR-0004), #2 (ADR-0005),
#3 (ADR-0006), #4 (ADR-0019), #6 and #7 (ADR-0021), #9 (ADR-0008),
#11 (ADR-0009).

## 5. Packet loss in v1?

The research disagrees. The market research puts jitter and packet loss in
the MVP (via WebRTC, as Cloudflare does). The methodology research says leave
loss out of v1: WebRTC means ICE, DTLS and SCTP on the server, which is
expensive in code and footprint, and WebTransport datagrams are the lighter
route later. Recommendation: jitter in v1, packet loss in v1.1 over
WebTransport.

## 8. Hardware floor and ceiling

Recommendation: the floor is a Raspberry Pi 4 (arm64, 1 GB) and the ceiling is
a 10 GbE LAN; browsers top out near 2.5 Gb/s, so 10 Gb/s needs a native client
or a node-to-node test. Confirm, or name the boxes it has to run on (the TrueNAS
host, circuit, a Pi?).

## 10. Native app

Which platform first, and when? Recommendation: iOS after the web UI ships,
since the Mac mini can build it. The API is designed for it either way
(ADR-0002).

