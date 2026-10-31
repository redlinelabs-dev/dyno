# ADR-0013: Telemetry opens as a centred dialog on wide screens

Status: accepted 2026-10-31. Amends ADR-0004, which made telemetry a bottom
sheet. The owner looked at the Speed over time charts on a desktop and said
"I feel like this would be better as a modal".

## Decision

- On viewports at least 700 px wide and 560 px tall, telemetry is a dialog
  centred over a darker scrim:
  - up to 920 px wide;
  - 24 px from each viewport edge;
  - rounded on all four corners, with no grab handle.
- On smaller viewports it stays a bottom sheet. On a phone that's the familiar
  pattern and keeps the tabs within thumb reach.
- Only the layout changes. It still has the same tabs. Escape, Close and a
  click on the scrim dismiss it, and the page behind is inert while it's open.

## Consequences

- A tall tab (Speed over time, History) scrolls inside the dialog rather than
  the page.
- `prototypes/dyno-ui.html` still shows the sheet on every size. This ADR is
  the spec for the wide layout.
