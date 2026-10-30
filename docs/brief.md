# Brief

The owner's original request, verbatim, 2026-10-30. This is the source of truth
for what "done" means. When a later doc disagrees with it, this file wins unless
an ADR records the owner changing their mind.

> I want to build a new version of an open source self-hosted speedtest
> application. I want something that generally speaking is a pretty to look at,
> and as easy to use as fast.com - but I want to add in options that things like
> openspeedtest and librespeedtest do (or more to the point what people who rely
> on those services are clamoring for) and integrate them in.
>
> I hate the user interfaces of those self hosted containers.
>
> At a high level this is what I consider "done"
>
> 1. containerized - can self-host via a simple compose.yaml file
> 2. MINIMAL CPU and memory needs
> 3. gorgeous user interface
> 4. progressive disclosure of more advanced features
> 5. can easily run on a mobile phone and/or a desktop browser
> 6. can have a mobile app in the future if I so desire - so the architecture
>    should be nimble enough to be powered by a web interface or a native
>    application
> 7. potentially can be turned into a "freemium" product where for homelabs
>    etc... this could be used completely free of charge - but if businesses
>    want to start relying on it, I could provide a licensed version
>
> Start by building prototypes for me to sign off on what the user interface
> should look like.
>
> Do research into the software needs to build this, and do evaluation to
> determine what platform gives us the best rock-solid performance, for the
> least amount of resources. I want the smallest memory and CPU footprint
> possible, with no memory leaks.
>
> Grill me for any questions you might have. As for the UI don't ask me what I
> want, build some prototypes first then let me decide.
>
> Start a github repository for this on this machine, seed it with all of these
> instructions, then prepare the repository for me to start a new claude code
> session that will start to build it. I believe this should be written inside
> the redlinelabs-dev group in github

## Acceptance criteria, restated as checks

| # | Criterion | How we check it |
|---|---|---|
| 1 | Containerized, simple compose | `docker compose up -d` with the shipped `compose.yaml` and no other files gives a working test at `http://host:port`. No `.env` required. |
| 2 | Minimal CPU and memory | Idle RSS, RSS under load, CPU per GB transferred, and a soak-test RSS slope are measured in CI or a repeatable bench script, with budgets written in `docs/adr/`. A regression fails the build. |
| 3 | Gorgeous UI | The owner signs off on a direction from `prototypes/`. |
| 4 | Progressive disclosure | The first screen holds one action and one number. Every advanced feature is reachable, but none of it is visible until asked for. |
| 5 | Phone and desktop | Works and looks right at 360 px wide and on a desktop; tested in mobile Safari and Chrome on Android. |
| 6 | Native app later | The web UI uses only the public HTTP API, the same one a native app would use. No server-rendered state the API can't reach. |
| 7 | Freemium | The licence and the free/paid split are chosen (see `docs/decisions-pending.md`), and the code keeps paid features in a separable place. |
