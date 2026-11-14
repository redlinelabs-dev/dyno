# ADR-0009: dyno's own identity, "by Redline Labs" as a small mark

Status: accepted 2026-10-30 by the owner (was decisions-pending #11): "We also
can just keep 'by Redline Labs' for now - I don't really have a robust brand
identity for Redline Labs yet".

## Decision

The signed-off Fast lane look (ADR-0004) is dyno's identity: the carbon and
bone palette, the red zone, the orange needle, Big Shoulders Display numerals
and the italic `DYNO/` mark. Redline Labs appears only as a small
"by Redline Labs" next to that mark. Nothing in the UI depends on a Redline
Labs brand system, because there isn't one yet.

## Consequences

- No restyle: the colours in CLAUDE.md's Brand rule stay as dyno's palette.
- If Redline Labs gets a real identity later, the mark is one element to
  change, not a theme.
- Fonts are self-hosted in the binary (both are under the SIL Open Font
  License), so the page makes no third-party requests on a LAN.
