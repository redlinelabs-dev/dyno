# ADR-0011: Big readouts instead of pills

Status: accepted 2026-10-30. The owner asked for a design pass, pointing at
fast.com: "I like the big bold numbers and letters here". This changes the
signed-off Fast lane screen (ADR-0004) under the dial; the dial itself is
unchanged.

## Decision

- Latency (idle and busy) and upload sit under the dial as large readouts:
  Big Shoulders 900 at up to 64 px, with a small Barlow label and unit. They
  replace the prototype's 22 px pills.
- Each group is a button with a 2 px rule in its colour from the Speed over
  time chart: needle orange for latency, bezel grey for upload. Latency
  opens the Latency tab, Upload opens Speed over time.
- The dial's unit is Big Shoulders 800 in bone instead of small grey text.
- Nothing moves while a value changes:
  - Big Shoulders has no tabular figures, so every live number puts each digit
    in a box as wide as the widest digit (`.tn`, set by `setNum()`).
  - Each figure is right-aligned in a slot sized for "999" ms or "9,999" Mbps,
    so it grows leftward and its unit stays put. Labels are right-aligned to
    match, as for a table column of figures.
  - Groups and columns have fixed tracks.
- A value that doesn't exist yet shows as a dim dash, not a bone one.

## Consequences

- Below about 440 px wide, Upload stacked under Latency. Superseded by ADR-0016: the readouts scale with the dial and stay side by side.
- Latency of 1,000 ms or more, or upload of 10 Gbit/s or more, overflows its
  slot and widens it. Both are rare enough to leave.
- `prototypes/dyno-ui.html` still shows the pills. This ADR is the spec for
  this part of the screen.
