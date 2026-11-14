# ADR-0015: Accessibility target is WCAG 2.2 AA

Status: accepted 2026-10-31. The owner asked to "make sure the app is a11y
compliant".

## Decision

The web UI meets WCAG 2.2 level AA. How each part of the UI does it:

- **Structure:**
  - The page has one `h1`.
  - Telemetry is a `dialog`, and the page behind it is `inert`.
  - Its tabs follow the WAI-ARIA tabs pattern: roving tabindex, arrow keys,
    Home and End, and a `tabpanel` labelled by the selected tab. Opening the
    dialog focuses the selected tab, and closing it returns focus to the
    opener.
- **Live numbers:** each digit sits in a fixed-width box for the eye
  (ADR-0011), which some screen readers read digit by digit. The boxes are
  `aria-hidden`, with the plain figure beside them for screen readers.
- **Announcements** go through one polite live region at the start, when
  download finishes, and at the end, a stop or a failure. Changing numbers are
  never live, so they don't chatter.
- **Chart:**
  - The plots are `aria-hidden` pictures.
  - The legend tables carry every result and peak as text.
  - A visually hidden `slider` steps through the samples with the arrow keys,
    and its `aria-valuetext` reads time, phase, speed and latency. Focusing it
    shows the same crosshair and tooltip as hovering.
  - Escape dismisses the tooltip before it closes the dialog (WCAG 1.4.13).
- **Contrast:**
  - Text is 4.5:1 or better.
  - Large figures are at least 3:1, including the dim "no value yet" dash
    (#62666F).
  - Chart marks are at least 3:1 against the panel.
  - Selected toggles use deep red (#8E141B), because bone on red is 3.95:1.
- **Motion and timing:**
  - The auto-started test can be stopped (ADR-0014).
  - Reduced motion turns off the dial easing, the blink, the dialog
    animation and the mark fade.
- **Reflow:** nothing scrolls sideways at 320 px except the History table,
  which is a focusable, labelled region.

## How it was checked

- axe-core 4.10 (WCAG 2.0, 2.1 and 2.2 A/AA plus best practice) ran in
  headless Chromium on the main screen and all four tabs, at 1280 and 390 px
  wide. It found no violations.
- axe left text over SVG and chart fills for review. Those pairs were worked
  out by hand.
- Keyboard paths and announcements were scripted in Playwright.

Not yet checked: real screen readers (VoiceOver, NVDA, TalkBack), Windows
forced-colours mode, and 200% browser zoom on a phone.

## Consequences

- Every UI change keeps this bar. Re-run the axe pass and the keyboard paths
  after touching the main screen, the dialog or the chart.
