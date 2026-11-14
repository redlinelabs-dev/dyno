# SEO and page-metadata audit of the web UI

Audited 2026-10-31 against the redline `seo` skill's practice list (snapshot
2026-08-02, in window). Evidence is the built `server/internal/webui/dist/`
and live responses from `bin/dyno` at commit 7c689b4 plus the uncommitted M2
work.

## What kind of site this is

Every dyno page is someone's own server, usually on a LAN or tailnet.
Redline Labs hosts no instance (ADR-0018), so no dyno instance is a page we
want in search results. An instance that is reachable from the internet and
gets indexed shows a stranger the owner's test history (`/api/v1/results` has
no auth yet) and invites them to spend the owner's bandwidth. Search
visibility for dyno itself belongs to the project's public pages (the GitHub
repo now, a landing page later), which don't exist yet. So the
ranking practices below are mostly n/a, and the work is the page metadata
every browser, phone home screen and link preview reads, plus an explicit
"don't index" (ADR-0023).

## Before

| Practice | Verdict | Evidence |
|---|---|---|
| Indexable, 200, snippet-eligible (AI features = Search eligibility) | n/a, deliberately not indexed | Private instances. `GET /` was 200 with no robots meta and no `robots.txt` (404), so a public instance was indexable by default. Gap: no explicit policy. |
| Canonical URL in served HTML | n/a | Each instance has its own host; a canonical would point at a host the build can't know. |
| Unique, descriptive title and meta description | gap | `<title>dyno speed test</title>`; no `<meta name="description">`. |
| Sitemap with honest `lastmod` | n/a | One page, not indexed. |
| robots.txt manages crawling, never blocks rendering resources | gap | `GET /robots.txt` → 404. |
| AI-crawler decision recorded in robots.txt | gap | No robots.txt. |
| Core Web Vitals (LCP, INP, CLS at p75 field) | n/a for field data | Private instances have no CrUX data. Lab: page is 487 B of HTML plus one CSS and one JS chunk; fonts `font-display: swap`. |
| Real HTML, title/description/noindex in source, not via JS | partial | Title is in source; body is drawn by JS (`<main>` holds only a `<noscript>`). Fine for an app that isn't indexed; noindex must go in source. |
| Structured data (BreadcrumbList, WebSite, SoftwareApplication, ProfilePage) | n/a | No hierarchy, not a site root we want named in Search, no genuine third-party ratings. |
| Internal links are real `<a href>` with descriptive anchors | pass | No navigation links; tabs are buttons in a dialog (ADR-0013). |
| One dominant heading, semantic HTML | pass | One `h1.v-logo` with screen-reader text "dyno speed test"; `h2`/`h3` inside the sheet. |
| `lang` and `viewport` | pass | `<html lang="en">`; `width=device-width, initial-scale=1, viewport-fit=cover`. |
| Favicon | gap | `GET /favicon.ico` → 404; no `<link rel="icon">`. Browsers show a blank tab icon. |
| Home-screen icon and manifest (phones) | gap | No `apple-touch-icon`, no manifest. "Add to Home Screen" on iPhone makes a screenshot tile. |
| `theme-color` / `color-scheme` | partial | `theme-color` #0F1115 present; `color-scheme: dark` only in CSS, so the first paint before CSS can flash light. |
| Accessibility (WCAG 2.2 AA, ADR-0015) | pass | axe-core 4.10.3 run on 2026-10-30: 0 violations on the main view and every sheet tab. Re-run after this change. |
| Measurement (Search Console, Bing Webmaster Tools) | n/a | Nothing is meant to be indexed. Applies when the public landing page exists. |

## After

| Gap | Now | Verified by |
|---|---|---|
| No indexing policy | `<meta name="robots" content="noindex">` in source; `robots.txt` allows `/`, disallows `/api/`, `/dl`, `/ul`, `/ping` (ADR-0023) | `TestIndexIsNoindex`; `GET /robots.txt` 200 `text/plain` |
| Title and description | `dyno speed test \| Redline Labs`, matching the `h1`; one-sentence description | Built `dist/index.html`; Chromium `page.title()`; `TestRoutes` |
| robots.txt and AI-crawler decision | Served at `/robots.txt`; AI crawlers get the same rules as everyone else, stated in a comment | `TestFixedNameFiles` |
| Favicon | `icon.svg` plus a 16/32/48 px `favicon.ico`, linked and at the fixed path | `TestFixedNameFiles`; contact sheet at 16 to 128 px on light and dark tab strips |
| Home-screen icon and manifest | `apple-touch-icon.png` (180 px), `manifest.webmanifest` with 192 and 512 px icons, `display: standalone`, served as `application/manifest+json` | Chromium `Page.getAppManifest`: no errors; `Page.getInstallabilityErrors`: none |
| `color-scheme` before CSS | `<meta name="color-scheme" content="dark">` | Built `dist/index.html` |
| Accessibility re-check | No change needed | axe-core 4.10.3, WCAG 2.0–2.2 A/AA plus best practice: 0 violations at 1280 and 390 px on the main view and all four sheet tabs |

Also added: `format-detection: telephone=no`, so iOS doesn't turn readouts
like "1,234" into phone links, and the phone sheet's top clears the status bar
when dyno runs from the home screen.

Measurement: nothing is meant to be indexed, so Search Console and Bing
Webmaster Tools have nothing to report on an instance. To confirm the policy
holds on a public instance, use Search Console's URL Inspection, which reports
"Excluded by 'noindex' tag". Once the public landing page exists, verify it in
both consoles and audit it against this list.

Unverified: Add to Home Screen on a real iPhone and Android phone (icon,
standalone launch, status bar over the dial).
