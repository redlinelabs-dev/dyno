# ADR-0016: The first screen scales to fill the window

Status: accepted 2026-10-31. The owner, looking at a 1622 × 1270 window
where the dial was capped at 540 px and pinned to the top: "the size of the
UI should take up 100% of either the height or the width, and this is taking
up 100% of neither."

## Decision

- **One composition:** the dial, readouts and actions are drawn at 540 design
  px wide and scaled as a whole. `--p` is one design px. Under the header it
  takes the largest size that fits and is centred, so it fills the height on
  desktops and landscape tablets and the width on phones and portrait
  tablets. There is no upper cap.
- **Minimum sizes:** small text keeps a floor of 12–14 px, so the
  composition's shape changes slightly as it shrinks. Because of that, `fit()`
  in `main.ts` finds the scale by bisection, measuring real layout, rather than
  from a fixed aspect ratio. It runs on resize and when fonts load, never
  during a test, so nothing moves while numbers change (ADR-0011).
- **Short, wide windows** (a phone on its side) also try a layout with the
  readouts beside the dial, and keep it when the dial comes out more than 10%
  bigger.
- **Readouts stay side by side at every width.** This replaces ADR-0011's
  stacking below 440 px.
- **Message space:** the message line under the actions keeps two lines of
  space, so a "Test stopped" or warning message doesn't shift the layout.

## Consequences

- The page never scrolls on the first screen.
- On very large monitors the dial is very large. This is deliberate, and it
  reads well across a room.
- On narrow phones the readout row is the widest part and sets the scale, so
  the dial sits a few pixels inside the full width.
