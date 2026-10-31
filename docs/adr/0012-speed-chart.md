# ADR-0012: Speed over time as two panels on one time scale

Status: accepted 2026-10-30. The owner found the first chart hard to read and
asked for "something more grafana-like". The first chart drew filled areas with
no axes, and its latency line shared the throughput plot on a hidden second
scale.

## Decision

- **Two panels share one time axis:** Throughput (download and upload) and
  Latency. Each has its own labelled y axis. There is never a second y scale on
  one plot.
- **Look:**
  - Solid hairline grid.
  - Lines 2 px, drawn as monotone cubics, so they're smooth but never overshoot a
    sample.
  - Under each line, the series colour fading from about 25% to nothing.
  - Download is red, upload bezel grey, latency orange.
- **Shading:** the warm-up at the start of each transfer is shaded in both
  panels. A dashed line marks each direction's result across the part that
  counted. Together they explain why a result can sit below the peak.
- **Idle reference:** the latency panel draws the idle median as a dashed
  reference line. The gap between it and the probes is bufferbloat.
- **Legend:** each panel has a legend table. Throughput shows Result and Peak;
  latency shows Idle, Downloading, Uploading and Peak. Values are always
  readable without hovering.
- **Crosshair:**
  - It snaps to the nearest sample and appears in both panels.
  - The tooltip shows time, phase (with warm-up), speed and latency.
  - With the chart focused, arrow keys step through the samples.
- **Spans:** the worker records each transfer's planned timing in
  `detail.spans` (`[dir, start, counted, end]`, seconds), so saved results
  carry their own phase marks. Results without spans get phase extents from
  their samples and no warm-up shading.
- **Phones read it a transfer at a time** (amended 2026-10-31). The owner
  found the shared timeline "disconnected somewhat from a 'flow' perspective
  on mobile" and picked this layout. Below 600 px of viewport width the tab
  shows a Download section, then an Upload section, in the order the test
  ran. Each section has:
  - its own time axis, from the start of the transfer to its end;
  - its throughput plot with Result and Peak under it;
  - a "Latency while downloading" (or uploading) plot with Loaded, Idle and
    Peak under it.

  Both sections keep the same y scales, so their heights compare. The
  crosshair, tooltip and arrow keys work as on the wide layout. The crosshair
  shows in the section that holds the moment.

## Consequences

- Upload in bezel grey fails the dataviz validator's chroma floor: it reads as
  neutral beside red. This is deliberate. Download is the headline and upload
  is secondary, and the two pass the colour-blind and contrast checks.
- Plots are SVG stretched to their box. Axis labels, phase labels and the
  crosshair are HTML laid over it in percentages, so text never distorts.
- During a run the chart redraws every frame. `#ch` is persistent, so keyboard
  focus survives.
