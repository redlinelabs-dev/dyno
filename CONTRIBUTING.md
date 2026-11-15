# Contributing to dyno

Thanks for looking. Issues are open: bug reports, measurements that look
wrong, install problems on your hardware, and feature requests are all
welcome.

## Pull requests

dyno is licensed under the GNU AGPL v3 (`LICENSE`). Contributions will need a
contributor license agreement, so that Redline Labs can also offer the code
under other terms. That agreement isn't set up yet. Until it is, pull requests
from outside can't be merged. If you have a fix, open an issue describing it,
and link a branch if you like.

## Building

`make check` runs everything CI runs: formatting, vet, the Go tests under the
race detector, the web UI's typecheck and tests, the installer tests and a
quick footprint check. You need Go and Bun. See `CLAUDE.md` for the layout and
the rules the code keeps, such as the memory budgets in
`docs/adr/0006-go-server.md`.
