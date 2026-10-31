# ADR-0023: Page metadata, icons, and no search indexing

Status: accepted 2026-10-31. The owner asked to "improve the SEO
characteristics of this site... meta attributes, a great title, favicon,
a11y". The audit is `docs/research/seo-audit.md`.

## Decision

- **A dyno server's page asks not to be indexed.** Every instance belongs to
  whoever runs it, and Redline Labs hosts none (ADR-0018), so no instance is a
  page dyno wants in search results. An indexed instance would show strangers
  the owner's test history (the API has no auth yet) and invite them to spend
  the owner's bandwidth. `index.html` carries `<meta name="robots"
  content="noindex">`. `robots.txt` lets crawlers fetch `/` so they see that,
  and keeps them out of `/api/`, `/dl`, `/ul` and `/ping`. AI crawlers get the
  same rules as everyone else. Search visibility for dyno itself belongs to
  the project's public pages, once they exist (ADR-0021).
- **Title and description:** "dyno speed test | Redline Labs", matching the
  page's `h1` (screen-reader text "dyno speed test", then "by Redline Labs"),
  and a one-sentence description that link previews show.
- **Icon:** a numberless gauge (brand rule) on a carbon tile, bone arc, red
  zone, needle orange needle, in `web/public/icon.svg`. `bun web/icons.ts`
  renders the PNGs and `favicon.ico` from it with headless Chromium; the
  outputs are committed so a build needs no browser.
- **Add to Home Screen opens dyno as an app.** `manifest.webmanifest` has
  `display: standalone`, the 192 and 512 px icons and the page colours; iOS
  gets `apple-touch-icon.png`, the title "dyno" and a translucent status bar.
  The stage and the phone sheet already pad by the safe-area insets, so
  content stays clear of the notch.
- **Fixed names:** `/favicon.ico` and `/robots.txt` are fetched by
  convention and the manifest names its icons, so `web/build.ts` copies
  `web/public/` into the build as is. The server registers the
  `application/manifest+json` type, which Go doesn't know.
- **Left out:** a canonical URL and Open Graph tags need the instance's own
  host, which the build can't know; structured data and a sitemap have no
  purpose on a page that isn't indexed.
- **No service worker.** Most instances are plain `http://` on a LAN, where
  browsers won't register one. A worker that controls the page sits in front
  of `/dl`, `/ul` and `/ping` and would add its own overhead to the
  measurement. A test can't run offline, a cached app shell would outlive a
  server upgrade, and neither Chromium nor iOS needs one to install the app.
  Alerts go out through ntfy, webhooks and Apprise (ROADMAP M3), not web push.
  The test engine's dedicated Web Worker (ADR-0010) is a different thing and
  stays.

## Consequences

- Someone who wants their instance in search results has to edit
  `index.html` and `robots.txt`. Nobody has asked for that.
- Standalone mode on a real iPhone is unverified: headless Chromium checks
  the manifest but can't launch a home-screen app.
