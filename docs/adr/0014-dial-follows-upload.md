# ADR-0014: The dial follows upload while it runs, and a running test can be stopped

Status: accepted 2026-10-31. Amends ADR-0004 (the ring is the download number)
and ADR-0011 (the upload readout counts live). The owner asked: "should both
download and upload metrics take up the guage when they are actively
running?" They agreed to the design below.

## Decision

- **During upload** the dial shows live upload speed:
  - Its fill is the upload colour (bezel), and the label reads "Measuring
    upload".
  - The plan zone's red overflow is off, because the plan speed is a download
    speed.
  - A red mark on the ring holds where download landed.
- **When the test ends** the dial sweeps back to the download result, which
  stays the headline. The mark goes away.
- **The Upload readout** shows a dim dash during upload and the server-counted
  result once done, so the live figure isn't on screen twice.
- **Stop:** while a test runs, the main button reads "Stop test" and stops it.
  Afterwards it reads "Test again". The test starts on its own and animates for
  10 to 30 s, and WCAG 2.2.2 requires a way to stop that (ADR-0015).
- **A stopped or failed test** shows a dash on the dial rather than 0.

## Consequences

- The screen is visibly busy for the whole test, not only for its first half.
- During upload the download figure is only the red mark. Its number is
  announced to screen readers ("Download 888 megabits per second. Measuring
  upload.") and returns to the dial at the end.
