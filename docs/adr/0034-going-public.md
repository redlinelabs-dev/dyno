# ADR-0034: Going public: the license text, headers, and contributions before the CLA

Status: accepted 2026-11-15. The owner chose the timing ("ok let's make it
public with the license you recommend"). The rest was settled by the build
session, on the owner's delegation of non-UI calls (see ADR-0025). This ADR
carries out ADR-0021 and changes when it applies: the repo goes public before
the circuit install has been run, and before the CLA tooling exists.

## Decision

- **`LICENSE` is the GNU AGPL version 3, word for word from gnu.org.** The
  license is `AGPL-3.0-only`, not "or any later version". Redline Labs, not
  the FSF, decides whether a later version applies. The CLA (ADR-0021) already
  keeps other terms possible.
- **Source files carry no license header.** The repo's root `LICENSE` covers
  every file, the way Immich and Plausible do it. A header in each file adds
  noise and drifts. A file under other terms, such as a future `pro/` module,
  carries its own `LICENSE` in its directory.
- **No outside pull request is merged until the CLA is in place.**
  `CONTRIBUTING.md` says so. Issues and discussion are open now. The CLA
  exists so Redline Labs can offer contributed code under other terms. If no
  outside code is merged, nothing needs relicensing, so going public before
  the tooling exists costs nothing. The CLA text (Apache ICLA or Harmony, per
  ADR-0021) and its pull-request check land with M4. So does the choice of
  who the grant goes to, which is the owner's to make.
- **Going public is quiet.** It isn't announced. The launch post, the
  community-scripts submission and the r/selfhosted README wait for M3's
  monitoring UI and M4.

## Why

- The owner wants the one-line installers to work for anyone, and they
  download from public GitHub releases. A private repo makes every documented
  install fail except for the owner.
- With no license file, public code would be "all rights reserved", and
  nobody could legally run it.

## Consequences

- The circuit install runs as a stranger's would: the public one-liner, with
  `DYNO_VERSION` set to the release candidate.
- The GHCR image has its own visibility setting, which only the owner can
  change in the package settings. Until it is public, the Docker install fails
  for everyone else.
- Pull requests from forks run CI with a read-only token and no secrets
  (`ci.yml` has `permissions: contents: read` and every job runs on
  GitHub-hosted runners). A first-time contributor's run needs the owner's
  approval, which is GitHub's default for public repos. No workflow uses
  `pull_request_target`, and none may without its own ADR.
- Still to do from ADR-0005 and the M4 list: the name clearance check, and
  a guard on the write endpoints before any dyno faces the internet.
