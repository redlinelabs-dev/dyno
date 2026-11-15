# ADR-0031: API timestamps always carry three fraction digits

Status: accepted 2026-11-14 by the build session, on the owner's delegation of
non-UI calls (see ADR-0025). Raised by the U2a black-box round (attack A11).
Not built yet: see Consequences.

## Decision

- Every time the API writes, in JSON and in CSV, is RFC 3339 in UTC with
  exactly three fraction digits: `2026-11-13T17:02:11.408Z`,
  `2026-11-13T17:00:00.000Z`. That covers `at`, `created_at`, `updated_at`,
  `next_run`, `last_success_at` and any time added later.
- Times the API reads (`from`, `to`) stay any RFC 3339 form, with or without
  a fraction, with `Z` or an offset.

## Why

- Today every time prints as Go's `RFC3339Nano`, which trims trailing zeros:
  `...11.408Z`, `...11.4Z` and `...11Z` are all possible, from the same field.
  That is valid RFC 3339, and browsers parse it.
- The first native app is iOS (ADR-0027). Apple's ISO 8601 parsers accept one
  form per configuration: with fractional seconds on, `...11Z` fails; with
  them off, `...11.408Z` fails. A field whose form depends on the
  millisecond would fail one time in a thousand, or every time on a whole
  minute such as `next_run`.
- One fixed width also keeps the CSV sortable as text and the docs' examples
  true.

## Consequences

- A small task before the native app work starts: one formatting helper the
  result, schedule and CSV writers share, its tests, and `docs/api.md`
  saying "RFC 3339 UTC with milliseconds". Clients that parse RFC 3339
  already read both forms, so the web UI needs nothing.
- Until then the trimmed form stays, consistently, everywhere.
