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
- Phones (amended 2026-10-31, after the owner called the phone layout
  "gross"):
  - The sheet is one height on every tab, 16 px short of the screen, so
    switching tabs doesn't make it jump. It used to stop at 84% of the height
    and showed the Speed charts in a third of the screen.
  - The tabs and Close stay pinned while a tab scrolls.
  - Dragging the sheet down from its top dismisses it, as the grab handle
    promises (the owner expected to swipe it away and instead scrolled the
    page behind). The sheet doesn't bounce, so content and sheet move as one.
    A drag that starts mid-scroll scrolls, and a sideways drag on the tab
    strip scrolls the tabs. Close and Escape remain the non-drag way out
    (WCAG 2.5.7). The page behind is locked while the sheet is open.
  - The bottom padding clears Safari's toolbar and the home indicator.
  - History drops the From column and stacks the date over the time, so it fits
    without scrolling sideways; the unit moves into the note under it.
  - The Speed tab shows one section per transfer (ADR-0012), not the shared
    timeline.
- Only the layout changes. It still has the same tabs. Escape, Close and a
  click on the scrim dismiss it, and the page behind is inert while it's open.

## Consequences

- A tall tab (Speed over time, History) scrolls inside the dialog rather than
  the page.
- `prototypes/dyno-ui.html` still shows the sheet on every size. This ADR is
  the spec for the wide layout.
