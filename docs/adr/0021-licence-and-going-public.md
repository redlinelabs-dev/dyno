# ADR-0021: AGPLv3 with a CLA; public once the installers work

Status: accepted 2026-10-31 by the owner, who picked both recommendations
after setting the goal in `docs/brief.md` (2026-10-31): "I want this to have
the potential to be a super popular self-hosted solution for folks." Answers
decisions-pending #6 (licence) and #7 (visibility).

## Decision

- **Licence: AGPLv3, with a contributor licence agreement.** Anyone can run,
  change and share dyno. Whoever runs a changed version as a network service
  has to publish their changes. The CLA lets Redline Labs offer the code under
  other terms too, which keeps a commercial licence or a pro module possible
  (the owner: "I don't _need_ to have this generate revenue I just want to
  leave the option open to").
- **The repo goes public once the installers work.** That means the Docker
  and Proxmox LXC installs from the brief's check #8 are built, documented and
  tested on circuit, and the licence and CLA are in place. A one-line
  installer and a public image need public downloads, so going public is
  also what lets them work.

## Why

- r/selfhosted readily adopts AGPL projects (Immich and Nextcloud are AGPL), and
  the AGPL stops a hosted competitor from taking dyno closed. Apache-2.0 would
  bring a little more adoption but no such protection. AGPL without a CLA
  would rule out dual licensing later.
- First impressions come from the install. A public repo with no working
  install path wastes the first round of attention.

## Consequences

- ADR-0034 (2026-11-15) adds `LICENSE` and `CONTRIBUTING.md`, settles headers,
  and takes the repo public before the CLA tooling exists.
- M4 adds the CLA. Use a standard text, such as the Apache ICLA or a Harmony
  agreement, with an automated check on pull requests. Pick the tool when it's built.
- Paid features still live in their own module (CLAUDE.md), so a later pro
  module costs no rewrite.
- Dependencies must be AGPL-compatible. Today the server needs only the Go
  standard library and `modernc.org/sqlite`, whose dependency tree is under
  BSD and MIT licences. Check each new dependency's licence before adding it.
- Once public, the fleet CI baseline and branch rulesets can be turned on
  (decisions-pending #7 said a private free-plan repo can't have rulesets).
