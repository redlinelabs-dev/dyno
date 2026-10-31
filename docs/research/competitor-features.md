# Competitor features and the requests they leave open

Researched 2026-10-31. This builds on `market-and-demand.md` (2026-10-30), which has the landscape, the licensing analysis and the accuracy complaints. This file adds what each project actually ships, read from READMEs, docs and source, and a deeper pass over issues and discussions with the maintainer's answer to each top request. Star, pull and reaction counts are snapshots from 2026-10-31, pulled through the GitHub REST and GraphQL APIs. "Signal" is total reactions on an issue or PR plus upvotes on a discussion.

Source notes. I found no prompt-injection attempts. Two things worth knowing when reading the numbers:

- LibreSpeed runs a stale bot ([stale.yml](https://github.com/librespeed/speedtest/blob/master/.github/workflows/stale.yml)). Most of its issues closed as `not_planned` between December 2025 and March 2026 carry the `no-issue-activity` label and only bot comments. They were ignored, not declined. henrywhitaker3 closed every open v1 issue as `not_planned` in one sweep on 2026-10-22 at 23:57 with no comment ([#402 timeline](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/402)). In both cases "closed" says nothing about the request.
- [OpenSpeedTest #166](https://github.com/openspeedtest/Speed-Test/issues/166) is a Chinese-language advert for a third-party "enhanced edition" behind a link redirector. It is spam, not an instruction to agents, and I didn't follow it. Netronome and henrywhitaker3 now triage with AI-written comments, which are labelled as such; I treated them as maintainer output.

Reddit and forums are still not covered (see `market-and-demand.md`). Everything here is GitHub plus project docs.

## 1. Who is in the field

Two kinds of product exist. **Test pages** run in the browser against your own server and keep little or no history. **Trackers** run a CLI on the server on a schedule and chart the results. dyno is aiming to be both. Only NetworkOptimizer does both today, and only for UniFi networks.

| Project | Kind | Stars | Last push | Latest release | Licence | Stack | State |
|---|---|---|---|---|---|---|---|
| [librespeed/speedtest](https://github.com/librespeed/speedtest) | Test page | 15,278 | 2026-10-23 | [v6.3.1](https://github.com/librespeed/speedtest/releases), 2026-10-21 | LGPL-3.0 | JS client, PHP backend | Active, AI-assisted maintenance in 2026 |
| [librespeed/speedtest-go](https://github.com/librespeed/speedtest-go) | Backend | 886 | 2026-08-17 | [v1.1.6](https://github.com/librespeed/speedtest-go/releases), 2026-04-30 | LGPL-3.0 | Go | First release in years |
| [librespeed/speedtest-rust](https://github.com/librespeed/speedtest-rust) | Backend | 253 | 2026-08-14 | v1.4.0, 2025-10-28 | LGPL-3.0 | Rust | Active |
| [librespeed/speedtest-cli](https://github.com/librespeed/speedtest-cli) | CLI | 853 | 2026-10-15 | v1.0.14, 2026-08-17 | LGPL-3.0 | Go | jsDelivr/Globalping helping maintain ([#86](https://github.com/librespeed/speedtest-cli/issues/86)) |
| [openspeedtest/Speed-Test](https://github.com/openspeedtest/Speed-Test) | Test page | 3,838 | 2026-04-22 (README only) | none; code is `app-2.5.4.js` from 2024 | MIT | Vanilla JS on nginx | App code frozen since 2024 |
| [openspeedtest/Docker-Image](https://github.com/openspeedtest/Docker-Image) | Image | 261 | 2026-10-25 | v2.0.8, 2026-10-25 | none detected | nginx-unprivileged | 59.4M pulls on [Docker Hub](https://hub.docker.com/r/openspeedtest/latest) |
| [alexjustesen/speedtest-tracker](https://github.com/alexjustesen/speedtest-tracker) | Tracker | 6,048 | 2026-10-26 | [v1.15.1](https://github.com/alexjustesen/speedtest-tracker/releases/tag/v1.15.1), 2026-10-22 | MIT | PHP, Laravel 13, Filament 5 | v1 frozen, v2 in progress on `2.x` |
| [gnmyt/MySpeed](https://github.com/gnmyt/MySpeed) | Tracker | 3,043 | 2026-10-30 (Dependabot) | [v1.0.9](https://github.com/gnmyt/MySpeed/releases), 2024-05-21 | MIT | Bun, Express, React | Maintainer "heavily inactive" ([disc#1285](https://github.com/gnmyt/MySpeed/discussions/1285)); features land on `development` only |
| [henrywhitaker3/Speedtest-Tracker](https://github.com/henrywhitaker3/Speedtest-Tracker) | Tracker | 1,647 | 2026-10-30 | [v2.7.2](https://github.com/henrywhitaker3/Speedtest-Tracker/releases), 2026-10-29 | MIT | Go, Nuxt | Rewritten from scratch 2026-10-22 to 10-29 |
| [autobrr/netronome](https://github.com/autobrr/netronome) | Tracker and monitor | 1,398 | 2026-10-25 | [v0.16.0](https://github.com/autobrr/netronome/releases/tag/v0.16.0), 2026-10-05 | GPL-2.0, paid themes closed | Go, React | 21 releases since 2025-06. Missing from `market-and-demand.md` |
| [Ozark-Connect/NetworkOptimizer](https://github.com/Ozark-Connect/NetworkOptimizer) | UniFi suite with all three test kinds | 1,218 | 2026-10-31 | v2.11.0-preview8, 2026-10-30 | BSL 1.1 | .NET 10, Go, OpenSpeedTest, iperf3 | Created 2025-12. Missing from `market-and-demand.md` |
| [XGHeaven/homebox](https://github.com/XGHeaven/homebox) | LAN test page | 821 | 2026-10-27 | v0.2.0, 2026-10-27 | none detected | React, Rust (actix) | Revived in 2026, now also an LLM benchmark |
| [MortyFx/speedtest-x](https://github.com/MortyFx/speedtest-x) | LibreSpeed fork | 1,951 | 2025-03-23 | 2024-10-06 | LGPL-2.1 | PHP | Dormant |
| [e7db/speedtest](https://github.com/e7db/speedtest) | Test page | 214 | 2023-03-04 | v0.5.3, 2021 | MIT | JS | Archived |

Adjacent projects that shape expectations without being direct competitors:

- [kavehtehrani/cloudflare-speed-cli](https://github.com/kavehtehrani/cloudflare-speed-cli) (1,082 stars, created 2025-12-26) is a Rust CLI and TUI against Cloudflare with idle and loaded latency, UDP loss and jitter with a MOS score, IPv4 against IPv6, history, JSON and CSV, and a headless mode. It is what a 2026 CLI ships.
- [geerlingguy/internet-pi](https://github.com/geerlingguy/internet-pi) (4,711) is Ansible for Prometheus, Grafana and a Speedtest.net exporter. It is the Grafana-native way people track speed today.
- [MiguelNdeCarvalho/speedtest-exporter](https://github.com/MiguelNdeCarvalho/speedtest-exporter) (239) runs the Ookla CLI on every Prometheus scrape ([exporter.py](https://github.com/MiguelNdeCarvalho/speedtest-exporter/blob/main/src/exporter.py)). No release since 2023.
- [itsDNNS/docsight](https://github.com/itsDNNS/docsight) (290, created 2026-02) is a "DOCSIS evidence system" for complaining to a cable ISP. It pulls speed results from speedtest-tracker's API rather than testing ([client.py](https://github.com/itsDNNS/docsight/blob/main/app/modules/speedtest/client.py)). There's an audience for ISP evidence, not only charts.
- [jason5ng32/MyIP](https://github.com/jason5ng32/MyIP) (12,272) has a speed-test card built on `@cloudflare/speedtest`. It never tests your own server.
- [openpacketloss](https://github.com/openpacketloss/OpenPacketLoss-Server) is a WebRTC packet-loss test with a Rust server and a built-in STUN server, MIT, published April 2026 by the OpenSpeedTest author. 22 stars across its three repos and no commits since April.

Searches for "networkquality server" and "rpm responsiveness" found nothing self-hosted with 100+ stars.

## 2. What each project ships

From READMEs, docs and source, not from issues. The matrix in section 4 condenses this.

### LibreSpeed (speedtest, -go, -rust, -cli)

- **Measures** download, upload, ping, jitter, IP, ISP and distance ([README](https://github.com/librespeed/speedtest#features)). No loaded latency. Since v6.2.0 (2026-07-23) a separate [`stability.html`](https://github.com/librespeed/speedtest#stability-test) runs repeated pings for a chosen time with min/avg/max, jitter, "failed request percentage", a live chart, threshold alerts and CSV export. The release calls this packet-loss tracking, but it counts failed HTTP requests.
- **Method.** XHR in a Web Worker. Download uses 6 streams by default, upload 3, with 1.5 s and 3 s grace times and up to 15 s per direction ([doc.md](https://github.com/librespeed/speedtest/blob/master/doc.md)). Upload is counted from `xhr.upload.onprogress` in the browser. `overheadCompensationFactor` defaults to 1.06, so every result is raised 6% ([speedtest_worker.js L63](https://github.com/librespeed/speedtest/blob/master/speedtest_worker.js)).
- **Storage.** Opt-in "telemetry" into MySQL, PostgreSQL, MSSQL or SQLite. A password-protected `stats.php` searches by ID or lists the last 100 tests. No charts or trends ([stats.php](https://github.com/librespeed/speedtest/blob/master/results/stats.php)). speedtest-go adds bolt and memory backends and a single-result JSON endpoint, with no list API ([web.go](https://github.com/librespeed/speedtest-go/blob/master/web/web.go)).
- **Multi-server.** A JSON list of test points, auto-picked by ping. Docker `MODE=frontend` and `MODE=backend`, and `SERVER_LIST_URL` since v6.3.0.
- **Sharing.** A PNG result image per test ID, restyled in [v6.3.0](https://github.com/librespeed/speedtest/releases/tag/v6.3.0).
- **Privacy.** `telemetry_level` defaults to none, `REDACT_IP_ADDRESSES`, a privacy page. ISP lookup calls ipinfo.io unless you give it an offline database or set `DISABLE_IPINFO` ([doc.md#privacy](https://github.com/librespeed/speedtest/blob/master/doc.md#privacy)).
- **UI.** Classic and modern designs behind a switch ([DESIGN_SWITCH.md](https://github.com/librespeed/speedtest/blob/master/DESIGN_SWITCH.md)). Classic follows the system dark setting. Modern is a fixed dark-purple theme with a background photo. `TITLE` and `TAGLINE` env vars. PWA manifest since v6.2.0.
- **Clients.** [speedtest-cli](https://github.com/librespeed/speedtest-cli) has `-4`/`-6`, interface binding, JSON/CSV output and several servers in one run. [speedtest-android](https://github.com/librespeed/speedtest-android) is a template for a branded app, last released 2021. No iOS app.
- **Deploy.** PHP on Apache or nginx, or GHCR images for amd64, arm64 and arm/v7 rebuilt weekly for PHP patches ([docker-publish.yml](https://github.com/librespeed/speedtest/blob/master/.github/workflows/docker-publish.yml)). The container still runs as root ([#682](https://github.com/librespeed/speedtest/issues/682)). Go and Rust backends ship single binaries for many platforms; speedtest-rust also ships .deb and a systemd unit.
- **Config** is spread over three places: JS parameters in the HTML, a PHP settings file and Docker env vars. speedtest-go uses `settings.toml` and 404s at `/` with its default config ([go#67](https://github.com/librespeed/speedtest-go/issues/67)).
- **Not shipped:** scheduling, notifications, Prometheus or any metrics endpoint, Home Assistant, Homepage.

### OpenSpeedTest (Speed-Test and Docker-Image)

- **Measures** download, upload, ping and jitter ([README](https://github.com/openspeedtest/Speed-Test#readme)). No loaded latency, loss, or IP display on self-hosted installs (`requestIP` is only on for their hosted channels, `app-2.5.4.js` L871-877).
- **Method.** 6 download and 6 upload XHRs, 12 s each, 30 MB upload bodies, 10 ping samples, all set as variables in `index.html`. `?XHR=1..32` changes streams. Upload is counted from `xhr.upload.onprogress`. Results are raised 4% by default (`upAdjust = dlAdjust = 1.04`); `?clean` turns that off ([README](https://github.com/openspeedtest/Speed-Test#new-features) item 5).
- **Stress mode.** `?Stress=Low…Year` or a number of seconds runs a long throughput test. `?Run`, `?Test=D/U/P`, `?Host=` drive it from a URL.
- **Storage.** None. A `saveData`/`saveDataURL` hook POSTs results to a URL you must write a receiver for: "no official support" ([#93](https://github.com/openspeedtest/Speed-Test/issues/93)).
- **Sharing.** The finished result links to `openspeedtest.com/results/show.php` with the numbers and the user agent in the query string ([app-2.5.4.js L1017](https://github.com/openspeedtest/Speed-Test/blob/main/assets/js/app-2.5.4.js)). The owner says this is by design ([#55](https://github.com/openspeedtest/Speed-Test/issues/55)).
- **UI.** One SVG. Dark mode follows the system with a cookie toggle. No branding config ("edit index.html yourself", [#9](https://github.com/openspeedtest/Speed-Test/issues/9)). No keyboard access ([#113](https://github.com/openspeedtest/Speed-Test/issues/113)).
- **Apps.** "OpenSpeedTest-Server" apps on the Microsoft Store, Mac App Store, iOS, Google Play and Snap, plus a Helm chart. These run the server on a device; the client is still a browser ([README](https://github.com/openspeedtest/Speed-Test#or-you-can-use-openspeedtest-server)).
- **Deploy.** Static files on any web server, or the Docker image on `nginx-unprivileged` (non-root) for six architectures. Built-in Let's Encrypt via `ENABLE_LETSENCRYPT`. The default HTTPS certificate uses `files/nginx.key`, a private key committed to the public repo ([tree](https://github.com/openspeedtest/Docker-Image/tree/main/files)). A small set of env vars: ports, `ALLOW_ONLY` CORS allowlist, `SET_SERVER_NAME`.
- **Not shipped:** history, scheduling, alerts, any integration, auth, CLI (promised in 2021, 2022 and 2023: [#2](https://github.com/openspeedtest/Speed-Test/issues/2), [#83](https://github.com/openspeedtest/Speed-Test/issues/83)).

### alexjustesen/speedtest-tracker (STT)

- **Engine.** The Ookla CLI only: `speedtest --accept-license --accept-gdpr --format=json` ([speedtest-process.md](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/other/speedtest-process.md)). `ResultService` has only `Ookla` and `Faker` on both `1.x` and `2.x` ([ResultService.php](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/app/Enums/ResultService.php)). Streams, protocol and duration are Ookla's choice.
- **Measures** what Ookla returns: download, upload, ping, ping jitter, per-direction jitter and loaded latency (avg/high/low), packet loss, ISP, external IP and server ([Prometheus docs](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/settings/data-platforms/prometheus.md)). It shows loaded latency but no bufferbloat grade.
- **History.** SQLite by default, or MariaDB, MySQL, Postgres. Charts for 24 hours, a week or a month ([HasChartFilters.php](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/app/Filament/Widgets/Concerns/HasChartFilters.php)), a `DEFAULT_CHART_RANGE`, min/max/avg stats since v1.12.0, comments on results, CSV export of selected results ([ResultExporter.php](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/app/Filament/Exports/ResultExporter.php)). Retention through `PRUNE_RESULTS_OLDER_THAN` in days.
- **Scheduling.** One cron expression in the `SPEEDTEST_SCHEDULE` env var. `SPEEDTEST_SKIP_IPS` skips runs on given WAN IPs. A pre-flight ping check since v1.13.0 ([env vars](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/getting-started/environment-variables.md)).
- **Alerts.** Absolute thresholds for download, upload and ping only ([ThresholdSettings.php](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/app/Settings/ThresholdSettings.php)). Triggers are "every scheduled run" or "threshold failure". Channels are in-app, mail, webhook and Apprise (v1.12.0). Discord, Gotify, Healthchecks, ntfy, Pushover, Slack and Telegram still work but show a "will be removed" banner ([NotificationSettings.php](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/app/Settings/NotificationSettings.php)). Apprise needs a separate apprise-api container.
- **Integrations.** `GET /prometheus` with an IP allowlist since [v1.11.0](https://github.com/alexjustesen/speedtest-tracker/releases/tag/v1.11.0) (2025-12-04). InfluxDB v2 push and bulk export. A REST API with Sanctum bearer tokens scoped to read results, run a test or list servers, rate-limited to 60/min, with an OpenAPI spec ([authorization.md](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/api/authorization.md), [routes](https://github.com/alexjustesen/speedtest-tracker/blob/1.x/routes/api/v1/routes.php)). Homepage reads an unauthenticated legacy endpoint kept for henrywhitaker3 compatibility. Home Assistant only through a [community integration](https://github.com/metaathron/ha-speedtest-tracker).
- **Auth.** Login required, default `admin@example.com` / `password`, admin and user roles, an optional read-only `PUBLIC_DASHBOARD`, `ALLOWED_IPS` ([authentication.md](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/security/authentication.md)). No OIDC or forward-auth.
- **Servers.** `SPEEDTEST_SERVERS` picks at random from a list of Ookla IDs; `SPEEDTEST_BLOCKED_SERVERS`; `SPEEDTEST_INTERFACE`. No per-server series and no own test server.
- **Missing by design of the engine:** live progress (Ookla returns JSON only at the end, [#2035](https://github.com/alexjustesen/speedtest-tracker/issues/2035)), IPv6 selection ([#615](https://github.com/alexjustesen/speedtest-tracker/issues/615)), LAN tests.
- **UI.** Filament admin panel with light and dark, Crowdin translations. Weak on phones ([#2402](https://github.com/alexjustesen/speedtest-tracker/issues/2402), closed not planned).
- **Deploy.** The LinuxServer.io image for amd64 and arm64 (67 MB compressed, 2.79M pulls). "Bare metal setups or Proxmox LXCs are not supported" ([installation](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/getting-started/installation/README.md)). Config is split: env vars for schedule, servers and pruning, the UI for thresholds and notifications, and threshold env vars "only take effect during initial setup".

### MySpeed

"Dev only" means on the `development` branch and in no release. The last release is v1.0.9 from 2024-05-21, and Docker `latest` is that build.

- **Engines.** Server-side CLIs downloaded at runtime: Ookla 1.2.0, librespeed-cli 1.0.10 and [cfspeedtest](https://github.com/code-inflation/cfspeedtest) ([binaries.js](https://github.com/gnmyt/MySpeed/blob/development/server/config/binaries.js)). Custom LibreSpeed server URL is dev only. No browser test.
- **Measures** ping, jitter, download, upload, duration and server ([Speedtests.js](https://github.com/gnmyt/MySpeed/blob/development/server/models/Speedtests.js)). No loss, no loaded latency.
- **History.** SQLite or MySQL. Views for 24 h, 48 h, 7 d and 30 d with averages. Released builds cap history at 30 days; dev makes it configurable up to forever ([settings.md](https://github.com/gnmyt/MySpeed/blob/development/docs/en/instructions/settings.md)). CSV/JSON export is dev only.
- **Scheduling.** Cron, pause and resume, and a random offset (dev) ([timer.js](https://github.com/gnmyt/MySpeed/blob/development/server/tasks/timer.js)).
- **Alerts.** Events only (test started, finished, failed). No thresholds ([#776](https://github.com/gnmyt/MySpeed/issues/776)). Channels: Discord, Gotify, Healthchecks.io, Pushover, Telegram, webhook; ntfy dev only ([integrations/](https://github.com/gnmyt/MySpeed/tree/development/server/integrations)).
- **Integrations.** `/api/prometheus/metrics` with Basic auth, undocumented ([prometheus.js](https://github.com/gnmyt/MySpeed/blob/development/server/routes/prometheus.js), [disc#1465](https://github.com/gnmyt/MySpeed/discussions/1465)). InfluxDB dev only. A Homepage widget ([gethomepage PR#3662](https://github.com/gethomepage/homepage/pull/3662)). An undocumented REST API.
- **Auth.** One password, optionally with read-only public viewing ([password.js](https://github.com/gnmyt/MySpeed/blob/development/server/middlewares/password.js)). Resetting it needs a SQL one-liner ([faq.md](https://github.com/gnmyt/MySpeed/blob/development/docs/en/faq.md)).
- **Multi-node.** "Nodes" shows other MySpeed instances in one UI by proxying their API ([nodes.js](https://github.com/gnmyt/MySpeed/blob/development/server/routes/nodes.js)). One test server per instance.
- **UI.** Light and dark since 2025-06, 15 locales, an OpenGraph share image.
- **Deploy.** Docker for amd64, arm64 and arm, a curl-bash installer, Linux binaries, a Windows build. Runs as root ([#941](https://github.com/gnmyt/MySpeed/issues/941)). Settings live in the web UI.

### henrywhitaker3/Speedtest-Tracker v2

- **Engine.** Ookla CLI only, and it refuses to start without `ACCEPT_EULA=true` ([CONFIGURATION.md](https://github.com/henrywhitaker3/Speedtest-Tracker/blob/v2/docs/CONFIGURATION.md)).
- **Measures** ping, download, upload, server and result URL. The raw Ookla JSON is kept, so jitter and loss exist in storage but not in the UI ([results.go](https://github.com/henrywhitaker3/Speedtest-Tracker/blob/v2/internal/results/results.go)).
- **History.** SQLite or Postgres, 24 h/7 d/30 d charts, `RETENTION_DAYS`. No min/max/avg, no export (it imports v1 backups).
- **Scheduling** through `TEST_SCHEDULE` cron. **Notifications** to Healthchecks.io, webhook, Telegram, Slack and Discord, on pre-run and post-run only, with no thresholds.
- **Integrations.** Prometheus `/metrics` on a separate port 8082 with health probes ([meta.go](https://github.com/henrywhitaker3/Speedtest-Tracker/blob/v2/internal/meta/meta.go)), OpenTelemetry export, an OpenAPI-documented API, a v1-compatible endpoint for Homepage.
- **Auth.** Optional OIDC with PKCE and an admin group claim, plus API tokens. Only manual runs and admin actions are gated; reads stay public ([AUTH.md](https://github.com/henrywhitaker3/Speedtest-Tracker/blob/v2/docs/AUTH.md)).
- **Deploy.** Distroless, non-root (65532), amd64 and arm64 on GHCR. Env-var config only. The README's compose file still points at the 2021 v1 image on Docker Hub.
- **Dropped from v1:** settings in the UI, threshold notifications, InfluxDB, CSV backup ([v1 README](https://github.com/henrywhitaker3/Speedtest-Tracker/blob/master/README.md)).

### Netronome

A server-side tester and network monitor. It has no browser test at all: no download, upload or ping endpoints in its routes ([server.go L226-349](https://github.com/autobrr/netronome/blob/develop/internal/server/server.go)).

- **Engines.** Three: Speedtest.net through the unofficial `showwin/speedtest-go` library (not the Ookla CLI), iperf3 with `-P` streams and `-R`, and LibreSpeed via `librespeed-cli` ([speed-tests.md](https://github.com/autobrr/netronome/blob/develop/documentation/src/content/docs/configuration/speed-tests.md), [iperf.go](https://github.com/autobrr/netronome/blob/develop/internal/speedtest/iperf.go)). Live progress in the UI.
- **Measures** download, upload, latency, jitter. Packet loss and per-hop RTT come from separate MTR/ping monitors ([packet-loss.md](https://github.com/autobrr/netronome/blob/develop/documentation/src/content/docs/monitoring/packet-loss.md)). Also DNS monitors, traceroute with GeoIP and ASN, and host stats from agents. No loaded latency.
- **History** charts, manual purge (no automatic retention), CSV/JSON export unreleased on `develop`.
- **Scheduling.** Intervals or fixed daily times, with random jitter ([scheduling.md](https://github.com/autobrr/netronome/blob/develop/documentation/src/content/docs/configuration/scheduling.md)).
- **Alerts.** Per-event rules with an operator and threshold (low speed, high ping, failure, packet loss, monitor down or recovered, agent offline, host resources), sent through a Shoutrrr fork to 26 services ([notifications.md](https://github.com/autobrr/netronome/blob/develop/documentation/src/content/docs/configuration/notifications.md)). No "N in a row" rule.
- **Integrations.** No Prometheus, Home Assistant or Homepage. The API needs a session cookie or a whitelisted IP.
- **Auth.** One local account, OIDC, and an IP/CIDR whitelist that skips login ([authentication.md](https://github.com/autobrr/netronome/blob/develop/documentation/src/content/docs/configuration/authentication.md)).
- **Agents.** `netronome agent` reports host stats and vnstat bandwidth, with Tailscale auto-discovery. Agents cannot run speed tests ([#98](https://github.com/autobrr/netronome/issues/98)).
- **Sharing.** A read-only public dashboard. Per-result links were declined ([out-of-scope note](https://github.com/autobrr/netronome/blob/develop/.out-of-scope/public-result-links.md)).
- **Theming.** Light, dark and system, plus premium colour themes for a one-time licence from $4.99 via Polar. The premium CSS is not in the source ([premium.ts](https://github.com/autobrr/netronome/blob/develop/web/src/constants/premium.ts), [themes.ts](https://github.com/autobrr/netronome/blob/develop/web/src/config/themes.ts)).
- **Deploy.** One binary for Linux, macOS, FreeBSD and Windows, deb/rpm/apk/Arch packages, and a GHCR image that bundles iperf3, librespeed-cli, mtr and vnstat. `config.toml` plus `NETRONOME__*` env vars; schedules, alerts and agents are set in the UI. Self-update command.

### NetworkOptimizer (UniFi only)

Ships three tests ([README](https://github.com/Ozark-Connect/NetworkOptimizer)). A WAN test from a Go Cloudflare-style binary measuring download, upload, latency, loaded latency and jitter with per-WAN history, optionally against your own OpenSpeedTest on a VPS. LAN tests with iperf3 between the gateway and devices over SSH. Browser tests through a bundled OpenSpeedTest, recorded per device. Scheduled tests alert on WAN degradation and on "LAN speed regression against recent baselines", and feed an adaptive SQM setting. BSL 1.1: free for personal use on up to 3 sites, paid for MSPs, and a hosted plan at "$15/month for up to 3 sites".

### homebox

A LAN test page with ping (10 HEAD requests), download and upload, a "high-speed mode" with up to 8 Web Workers that claims 12-20 Gbps, a continuous mode and rate charts ([README](https://github.com/XGHeaven/homebox#design)). Download and upload payloads are all zeros (`server/src/main.rs`, `web/src/cases/upload.ts`), upload is counted client-side, and there is no jitter. No history for network tests, no auth, no scheduling. Light and dark, Chinese and English. Single binaries for many platforms and an amd64/arm64 image (378k pulls).

## 3. What users ask for, per project

Feature requests only. Bugs are summarised after each table. "Response" is what the maintainer did.

### speedtest-tracker

The richest demand data in the category. The maintainer's own v2 plan, posted in [disc#2304](https://github.com/alexjustesen/speedtest-tracker/discussions/2304) (22 upvotes, 10 reactions, 31 comments, missed by `market-and-demand.md`), lists a redesigned and embeddable dashboard, several tests and schedules set in the UI, ping tests, alerts only after X failures, recovery and summary notifications, SSO, retry on another server, relative thresholds, test nodes and a headless mode. The most-upvoted user replies there ask for adaptive test intervals after a breach (8), pausing tests while other containers download (6), a faster UI on phones (4) and SLA reports as ISP evidence (3).

| Request | Signal | State | Response |
|---|---|---|---|
| Single-user mode, no login ([#1530](https://github.com/alexjustesen/speedtest-tracker/issues/1530)) | 39 | closed not planned, 2026-07-23 | "This will be a non-issue when SSO is introduced" |
| Other engines: iPerf, LibreSpeed, Cloudflare, fast, OpenSpeedTest ([disc#976](https://github.com/alexjustesen/speedtest-tracker/discussions/976)) | 31 | open since 2023 | Co-maintainer: ISPs trust Ookla. A nettest engine PR ([#2862](https://github.com/alexjustesen/speedtest-tracker/pull/2862)) was declined on 2026-08-06: "not looking to expand the scope of testing tools" |
| Default chart range ([#738](https://github.com/alexjustesen/speedtest-tracker/issues/738)) | 21 | completed | Built, `DEFAULT_CHART_RANGE` |
| Prometheus ([#53](https://github.com/alexjustesen/speedtest-tracker/issues/53), [disc#24](https://github.com/alexjustesen/speedtest-tracker/discussions/24)) | 24 | completed | Built in v1.11.0, three years after the request |
| Homepage widget ([#81](https://github.com/alexjustesen/speedtest-tracker/issues/81)) | 15, 41 comments | completed | Built through a legacy endpoint |
| Daily or weekly average notification ([#328](https://github.com/alexjustesen/speedtest-tracker/issues/328)) | 15 | open | "Not a priority until after v1". A report PR ([#2530](https://github.com/alexjustesen/speedtest-tracker/pull/2530)) closed unmerged |
| Continuous ping tests ([disc#2219](https://github.com/alexjustesen/speedtest-tracker/discussions/2219)) | 14 | open | On the v2 plan, not built |
| Headless, API-only service ([disc#2610](https://github.com/alexjustesen/speedtest-tracker/discussions/2610)) | 14 | open | Planned with Ookla, LibreSpeed, iPerf and ping. Only bearer auth and the server list are done |
| Min/max/avg and deltas ([disc#650](https://github.com/alexjustesen/speedtest-tracker/discussions/650), [#1244](https://github.com/alexjustesen/speedtest-tracker/issues/1244), [#243](https://github.com/alexjustesen/speedtest-tracker/issues/243)) | 26 | completed | Built in v1.12.0 and `/api/v1/stats` |
| Several cron schedules ([#1565](https://github.com/alexjustesen/speedtest-tracker/issues/1565)) | 11 | open | PR [#2842](https://github.com/alexjustesen/speedtest-tracker/pull/2842) open against 2.x |
| iPerf ([#64](https://github.com/alexjustesen/speedtest-tracker/issues/64)) | 9 | closed "completed", moved to disc#976 | Not built |
| Live progress ([#2035](https://github.com/alexjustesen/speedtest-tracker/issues/2035)) | 9 | open | "Technically not possible" with the Ookla CLI |
| Retest on another server after a breach ([#1815](https://github.com/alexjustesen/speedtest-tracker/issues/1815)) | 9 | open | On the v2 plan |
| Alert only after N breaches in a row ([#687](https://github.com/alexjustesen/speedtest-tracker/issues/687)) | 8 | open | On the v2 plan, two duplicates |
| Bufferbloat test ([#2262](https://github.com/alexjustesen/speedtest-tracker/issues/2262)) | 8 | closed "completed" 2025-12-20 | Closed as covered by Ookla's loaded latency. No grade or idle-vs-loaded delta was added |
| OIDC, disable auth, forward-auth ([PR#2801](https://github.com/alexjustesen/speedtest-tracker/pull/2801), [disc#391](https://github.com/alexjustesen/speedtest-tracker/discussions/391), [#620](https://github.com/alexjustesen/speedtest-tracker/issues/620), [#1412](https://github.com/alexjustesen/speedtest-tracker/issues/1412), [#336](https://github.com/alexjustesen/speedtest-tracker/issues/336)) | 26 | PR open, the rest not planned | "Filament … bakes in authentication pretty deeply". OIDC waits for v2 |
| Packet loss in thresholds ([#2155](https://github.com/alexjustesen/speedtest-tracker/issues/2155)) | 7 | open | PR [#2884](https://github.com/alexjustesen/speedtest-tracker/pull/2884) open against 2.x |
| IPv6 tests ([#615](https://github.com/alexjustesen/speedtest-tracker/issues/615)) | 6 | closed "completed" | Not possible with the Ookla CLI. Interface selection added instead |
| Retry failed tests ([#528](https://github.com/alexjustesen/speedtest-tracker/issues/528)) | 6 | open | "Never implemented". On the v2 plan |
| Server finder ([disc#1602](https://github.com/alexjustesen/speedtest-tracker/discussions/1602)) | 6 | open | Partly met by a server list tool |
| Failed tests and outages on graphs ([#509](https://github.com/alexjustesen/speedtest-tracker/issues/509)) | 5 | open | "No ETA … don't have the capacity". Gaps request [#1730](https://github.com/alexjustesen/speedtest-tracker/issues/1730) not planned |
| Hour-by-day heat map ([#1013](https://github.com/alexjustesen/speedtest-tracker/issues/1013)) | 5 | closed not planned 2026-07-23 | "Really cool idea", closed in the v1 freeze |
| A graph per server ([#1452](https://github.com/alexjustesen/speedtest-tracker/issues/1452), [#2586](https://github.com/alexjustesen/speedtest-tracker/issues/2586)) | 7 | open | Ignored |
| Relative thresholds, % of time below threshold ([#908](https://github.com/alexjustesen/speedtest-tracker/issues/908), [#1786](https://github.com/alexjustesen/speedtest-tracker/issues/1786)) | 7 | open | Ignored |
| Replace channels with Apprise ([disc#1961](https://github.com/alexjustesen/speedtest-tracker/discussions/1961) poll) | 16 yes, 5 no | open | Done in v1.12.0, older channels deprecated |

Bug themes: the scheduler silently stops ([#1065](https://github.com/alexjustesen/speedtest-tracker/issues/1065), 15 reactions, 79 comments; [#2887](https://github.com/alexjustesen/speedtest-tracker/issues/2887), 2026-09), Ookla rate limits and vanishing servers ([disc#2909](https://github.com/alexjustesen/speedtest-tracker/discussions/2909), 2026-10-30), scheduled results below manual ones ([FAQ](https://github.com/alexjustesen/speedtest-tracker-docs/blob/main/help/faqs.md)), the connectivity pre-check failing when ping or icanhazip is blocked ([#2560](https://github.com/alexjustesen/speedtest-tracker/issues/2560), 24 comments), notifications missing or duplicated ([#2332](https://github.com/alexjustesen/speedtest-tracker/issues/2332), 21 comments), and 500 errors after upgrades ([#2080](https://github.com/alexjustesen/speedtest-tracker/issues/2080)).

### LibreSpeed

No Discussions tab. No issue asks for Prometheus, scheduling or history: LibreSpeed users treat it as a one-shot test page.

| Request | Signal | State | Response |
|---|---|---|---|
| New UI with Tailwind ([#715](https://github.com/librespeed/speedtest/issues/715)) | 14 | not planned (stale) | A rewrite was underway; CDN dependencies refused because "LS needs to be a standalone thing". Redesign shipped as v6.0.0 |
| Dark mode ([#440](https://github.com/librespeed/speedtest/issues/440)) | 11 | completed | Built in 5.4.0 as system-following only, classic UI only |
| Learn from speed.cloudflare.com's statistics ([#503](https://github.com/librespeed/speedtest/issues/503)) | 9 | not planned (stale) | Ignored |
| UI not impressive ([#524](https://github.com/librespeed/speedtest/issues/524)) | 9 | completed | Answered by v6.0.0 (2026-04) |
| Bufferbloat via continuous ping ([#479](https://github.com/librespeed/speedtest/issues/479)) | 8 | not planned (stale, 2026-03) | Ignored |
| RPM responsiveness ([PR#562](https://github.com/librespeed/speedtest/pull/562), [#543](https://github.com/librespeed/speedtest/issues/543)) | 6 | closed unmerged (stale) | Praised as "very useful and well written", never merged. One user ran the fork "in production" |
| Docker tags per release ([#461](https://github.com/librespeed/speedtest/issues/461)) | 7 | completed | Built |
| iOS client ([#256](https://github.com/librespeed/speedtest/issues/256)) | 5, 14 comments | not planned (stale) | Wanted, never built |
| Single or multi-connection toggle ([#697](https://github.com/librespeed/speedtest/issues/697)) | 4 | not planned (stale) | Ignored |
| Non-root, read-only container ([#682](https://github.com/librespeed/speedtest/issues/682)) | 2, 15 comments | not planned (stale) | Agreed, volunteer gave up on the legacy build. Users objected to the closure |
| Total bytes moved ([PR#627](https://github.com/librespeed/speedtest/pull/627)) | 2 | closed (stale) | A collaborator wrote "Still a good idea. Don't close stale bot"; the bot closed it anyway |
| Video, gaming and VoIP assessment ([#454](https://github.com/librespeed/speedtest/issues/454)) | 1 | not planned (stale) | Ignored |
| Limit simultaneous tests ([#541](https://github.com/librespeed/speedtest/issues/541)) | 1 | not planned (stale) | "Hard to implement in a non abusable way" |
| speedtest-go works out of the box ([go#67](https://github.com/librespeed/speedtest-go/issues/67)) | 10 | open since 2023 | No reply |
| CLI still maintained? ([cli#86](https://github.com/librespeed/speedtest-cli/issues/86)) | 8 | closed | Maintainer too busy; jsDelivr/Globalping offered to take over |

Bug themes: inflated upload from client-side counting ([#819](https://github.com/librespeed/speedtest/issues/819), [#504](https://github.com/librespeed/speedtest/issues/504)), slow behind a proxy or TLS ([#686](https://github.com/librespeed/speedtest/issues/686), [#494](https://github.com/librespeed/speedtest/issues/494) with 30 comments), wrong client IP or ISP in Docker ([#615](https://github.com/librespeed/speedtest/issues/615), [#862](https://github.com/librespeed/speedtest/issues/862)), PHP CVE churn ([#866](https://github.com/librespeed/speedtest/issues/866), 10 reactions). The most-reacted non-request issue is about profanity in the UI ([#473](https://github.com/librespeed/speedtest/issues/473), 14).

### OpenSpeedTest

Reactions are tiny, so comment counts matter more here.

| Request | Signal | State | Response |
|---|---|---|---|
| Restart button ([#140](https://github.com/openspeedtest/Speed-Test/issues/140)) | 5 | open | "Great idea! I'll give it a try soon" (2024). Not built |
| Show the client IP, IPv4 vs IPv6 ([#160](https://github.com/openspeedtest/Speed-Test/issues/160)) | 4 | open, 2026-01 | Ignored |
| Ping during load ([#142](https://github.com/openspeedtest/Speed-Test/issues/142), [#33](https://github.com/openspeedtest/Speed-Test/issues/33), [#17](https://github.com/openspeedtest/Speed-Test/issues/17)) | 5, 15 comments | open; #33 not planned | "Next major rewrite". Workaround offered: run three browser windows. The requester "built my own speed test suite" (2026-08) |
| CLI client ([#2](https://github.com/openspeedtest/Speed-Test/issues/2), [#36](https://github.com/openspeedtest/Speed-Test/issues/36), [#54](https://github.com/openspeedtest/Speed-Test/issues/54), [#83](https://github.com/openspeedtest/Speed-Test/issues/83)) | 3, 16 comments | #83 open | Promised for "end of this year" in 2022, then "priorities have changed" |
| Several servers in one test ([#148](https://github.com/openspeedtest/Speed-Test/issues/148)) | 1 | open | "Working on a new version". Not shipped |
| Keyboard accessibility ([#113](https://github.com/openspeedtest/Speed-Test/issues/113)) | 1 | open since 2023 | "May rework the entire UI in next major release" |
| Results shown locally, not on openspeedtest.com ([#55](https://github.com/openspeedtest/Speed-Test/issues/55)) | 0, 9 comments | closed | Declined: a static page has no other way. User: "I'll do it myself" |
| Log results to a database ([#10](https://github.com/openspeedtest/Speed-Test/issues/10), [#93](https://github.com/openspeedtest/Speed-Test/issues/93)) | 0, 9 comments | #93 open | "Write your own code at server side" |
| Branding ([#9](https://github.com/openspeedtest/Speed-Test/issues/9), [#88](https://github.com/openspeedtest/Speed-Test/issues/88)) | 0, 10 comments | closed | "Edit index.html yourself" |
| Sub-millisecond ping ([#141](https://github.com/openspeedtest/Speed-Test/issues/141)) | 0, 11 comments | open | "Who cares about sub-ms?", then a flag was promised. Not built |
| Cloudflare-style multi-size tests ([#34](https://github.com/openspeedtest/Speed-Test/issues/34)) | 0 | not planned | "I feel like it's pointless" |
| Docker: versioned tags ([D#4](https://github.com/openspeedtest/Docker-Image/issues/4)), drop `VOLUME` ([D#24](https://github.com/openspeedtest/Docker-Image/issues/24)), config via env ([D#35](https://github.com/openspeedtest/Docker-Image/issues/35)), one UI for LAN and internet ([D#34](https://github.com/openspeedtest/Docker-Image/issues/34)) | 23 comments, 4, 0, 0 | tags built, rest open | Tags built reluctantly ("will confuse people"); the rest ignored |

Bug themes: impossible upload numbers ([#4](https://github.com/openspeedtest/Speed-Test/issues/4), 37 comments; [#121](https://github.com/openspeedtest/Speed-Test/issues/121)), reverse proxies and body limits ([#67](https://github.com/openspeedtest/Speed-Test/issues/67), [D#6](https://github.com/openspeedtest/Docker-Image/issues/6) with 36 comments), multi-gig ceilings ([D#15](https://github.com/openspeedtest/Docker-Image/issues/15)), and v2.0.7 crash-looping on a CRLF entrypoint ([D#41](https://github.com/openspeedtest/Docker-Image/issues/41), [D#42](https://github.com/openspeedtest/Docker-Image/issues/42), 9 reactions, fixed next day).

### MySpeed

The maintainer has not answered any open request below since posting in [disc#1285](https://github.com/gnmyt/MySpeed/discussions/1285) (2025-11-20) that he is "heavily inactive".

| Request | Signal | State | Response |
|---|---|---|---|
| Several test servers per instance ([#945](https://github.com/gnmyt/MySpeed/issues/945)) | 7 | open | Ignored. An ISP engineer runs five instances to work around it |
| iperf3 ([#1104](https://github.com/gnmyt/MySpeed/issues/1104), [#922](https://github.com/gnmyt/MySpeed/issues/922) with loss) | 8 | open | Ignored |
| 1.1.0 redesign feedback ([disc#744](https://github.com/gnmyt/MySpeed/discussions/744)) | 10 | | Owner promised ntfy, email and threshold alerts. Thresholds never shipped |
| Packet loss ([#1045](https://github.com/gnmyt/MySpeed/issues/1045)) | 4 | open | Ignored. A user sees 20-30% loss that MySpeed doesn't show |
| A new stable release ([#1369](https://github.com/gnmyt/MySpeed/issues/1369)) | 4 | open | Ignored. A fork now ships builds |
| OIDC ([#767](https://github.com/gnmyt/MySpeed/issues/767)) | 4 | open | Ignored |
| More than 30 days of history ([disc#1029](https://github.com/gnmyt/MySpeed/discussions/1029)) | 4 | | Built on dev 2026-05, unreleased |
| Threshold notifications ([#776](https://github.com/gnmyt/MySpeed/issues/776)) | 3 | open | Not built. "The one thing I am really missing coming from speedtest tracker" |
| Totals over long periods as ISP evidence ([#1088](https://github.com/gnmyt/MySpeed/issues/1088)) | 3 | open | Ignored |
| Non-root container ([#941](https://github.com/gnmyt/MySpeed/issues/941)) | 3 | open | Ignored |
| Prometheus docs, REST API docs ([disc#1465](https://github.com/gnmyt/MySpeed/discussions/1465), [disc#1009](https://github.com/gnmyt/MySpeed/discussions/1009)) | 5 | | Both exist in code, neither is documented |

Bug themes: the Cloudflare provider fails validation on fresh installs ([#1460](https://github.com/gnmyt/MySpeed/issues/1460), 9), the three engines give "drastically different results" ([#1419](https://github.com/gnmyt/MySpeed/issues/1419)), integrations break ([#1608](https://github.com/gnmyt/MySpeed/issues/1608) Gotify, [#1297](https://github.com/gnmyt/MySpeed/issues/1297) Discord), "eth0 not found" ([#806](https://github.com/gnmyt/MySpeed/issues/806)).

### henrywhitaker3/Speedtest-Tracker

All v1 requests were closed unanswered on 2026-10-22. The v2 rewrite answers some of them.

| Request | Signal | v2 status |
|---|---|---|
| Project abandoned, can someone take over ([#1013](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/1013)) | 27, 40 comments | Answered by the rewrite. alexjustesen's tracker grew out of this thread |
| Gotify ([#402](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/402), [#655](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/655)) | 11 | Not in v2 |
| Server blacklist ([#806](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/806)) | 9, 17 comments | Allow-list only |
| Pushover, email ([#603](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/603), [#825](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/825), [#339](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/339)) | 13 | Not in v2 |
| Second Y axis ([#462](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/462)) | 7 | Separate charts instead |
| Prometheus ([#658](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/658)) | 6 | Built |
| Date formats, units, rounding ([#965](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/965), [#788](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/788), [#684](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/684)) | 12 | Not in v2 |
| Tweet the ISP below a threshold ([#499](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/499)) | 4 | No thresholds in v2 |
| Jitter graph, median, more frequent pings ([#743](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/743), [#178](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/178), [#928](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/928)) | 9 | Not in v2 |
| Move off the Ookla CLI ([#590](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/590)) | 2 | Owner in 2021: would add a provider env var. v2 is still Ookla-only |

Bug themes (v1): "last X days" filter broken ([#816](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/816), 14), notifications not sending ([#891](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/891), 9), the Ookla binary missing ([#512](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/512), 39 comments).

### Netronome

97 issues and 4 discussions; the top request has 6 reactions.

| Request | Signal | State | Response |
|---|---|---|---|
| Maintained Shoutrrr ([#126](https://github.com/autobrr/netronome/issues/126)) | 6 | completed | Built in v0.14.0 |
| Separate agent image ([#141](https://github.com/autobrr/netronome/issues/141)) | 5 | wontfix | Declined: a second build matrix isn't worth it |
| Cloudflare engine ([#180](https://github.com/autobrr/netronome/issues/180)) | 4 | open | No reply in 9 months |
| Agents run scheduled speed tests ([#98](https://github.com/autobrr/netronome/issues/98), [#197](https://github.com/autobrr/netronome/issues/197), [disc#192](https://github.com/autobrr/netronome/discussions/192)) | 9 | open | "Will get to it at some point" (2025-08). A #197 commenter wants to "remove Speedtest-Tracker and run the speedtests through agents" |
| Traceroute monitors on the dashboard ([disc#185](https://github.com/autobrr/netronome/discussions/185)) | 4 | open | No reply |
| Public link per result ([#159](https://github.com/autobrr/netronome/issues/159)) | 2 | wontfix | Declined, because the engines don't report to a public results host |
| Alerts on averages, not single results ([#224](https://github.com/autobrr/netronome/issues/224)) | 0 | open | No reply |
| Pre- and post-test hooks, e.g. pause torrents ([#188](https://github.com/autobrr/netronome/issues/188)) | 0 | open | No reply |

Bug themes: results of zero or a tenth of real speed from the speedtest.net library ([#167](https://github.com/autobrr/netronome/issues/167), [#184](https://github.com/autobrr/netronome/issues/184), both open), Docker permissions on first run ([#140](https://github.com/autobrr/netronome/issues/140)), OIDC sessions dropping after minutes ([#95](https://github.com/autobrr/netronome/issues/95)).

### homebox

All requests have 0-1 reactions. Rate charts ([#25](https://github.com/XGHeaven/homebox/issues/25)) and arm64 ([#17](https://github.com/XGHeaven/homebox/issues/17)) were built. A mobile app ([#18](https://github.com/XGHeaven/homebox/issues/18)) and diagnosing bad latency ([#20](https://github.com/XGHeaven/homebox/issues/20)) were declined. Complaints are impossible uploads ([#22](https://github.com/XGHeaven/homebox/issues/22), 5.5 Gbps on 2.5G; [#18](https://github.com/XGHeaven/homebox/issues/18), "44Tbps!!!") and 12-15 ms ping on a LAN ([#38](https://github.com/XGHeaven/homebox/issues/38)).

## 4. Comparison matrix

dyno is the state of the `m2-web-ui` branch on 2026-10-31: M1 and M2 built, M3 and M4 not. LS is LibreSpeed with its Go and Rust backends and CLI. OST is OpenSpeedTest. STT is alexjustesen/speedtest-tracker v1.15. HW is henrywhitaker3 v2.7. MySpeed counts released v1.0.9 features as ✓ and `development`-only ones as "dev". "n/c" means not checked.

| Feature | LS | OST | STT | MySpeed | HW v2 | Netronome | dyno |
|---|---|---|---|---|---|---|---|
| **Testing** | | | | | | | |
| Browser test against your own server | ✓ | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ |
| Scheduled server-side tests | ✗ | ✗ | ✓ | ✓ | ✓ | ✓ | ✗, M3 |
| Engine | own | own | Ookla CLI | Ookla, LibreSpeed, Cloudflare CLIs | Ookla CLI | speedtest.net lib, iperf3, LibreSpeed | own |
| Download and upload | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Idle latency and jitter | ✓ | ✓ | ✓ | ✓ | ping only, jitter in raw JSON | ✓ | ✓ |
| Loaded latency | ✗ | ✗ ([#142](https://github.com/openspeedtest/Speed-Test/issues/142)) | ✓ from Ookla | ✗ | raw JSON only | ✗ | ✓ per direction |
| Bufferbloat grade or RPM | ✗ ([PR#562](https://github.com/librespeed/speedtest/pull/562) unmerged) | ✗ | ✗ ([#2262](https://github.com/alexjustesen/speedtest-tracker/issues/2262)) | ✗ | ✗ | ✗ | ✓ grade and RPM |
| Packet loss | partial: failed HTTP requests on `stability.html` | ✗ (separate OpenPacketLoss) | ✓ from Ookla | ✗ ([#1045](https://github.com/gnmyt/MySpeed/issues/1045)) | raw JSON only | ✓ MTR monitors | ✗, Later (pending #5) |
| Continuous ping or stability mode | ✓ `stability.html` | partial: throughput stress mode | ✗ ([disc#2219](https://github.com/alexjustesen/speedtest-tracker/discussions/2219)) | ✗ | ✗ | ✓ ping/MTR monitors | ✗ |
| Upload counted on the server | ✗ client progress | ✗ client progress | n/a (Ookla) | n/a | n/a | n/a | ✓ (ADR-0003) |
| Hidden correction factor by default | ✗ avoid: ×1.06 | ✗ avoid: ×1.04 | n/a | n/a | n/a | n/a | none |
| Stream count and test length choice | config only | URL params | ✗ | ✗ | ✗ | connections setting | ✓ Setup tab |
| Client-CPU-limited warning | ✗ | ✗ | n/a | n/a | n/a | n/a | ✓ |
| Live progress | ✓ | ✓ | ✗ ([#2035](https://github.com/alexjustesen/speedtest-tracker/issues/2035)) | n/c | n/c | ✓ | ✓ |
| Speed-over-time chart of one test | ✗ | ✗ | ✗ | ✗ | ✗ | n/c | ✓ (ADR-0012) |
| IPv4 or IPv6 choice | CLI `-4/-6` | ✗ | ✗ ([#615](https://github.com/alexjustesen/speedtest-tracker/issues/615)) | ✗ | ✗ | n/c | ✗ |
| Client IP and ISP shown | ✓ via ipinfo.io | ✗ self-hosted | ✓ | n/c | raw JSON | n/c | ✗ |
| **History** | | | | | | | |
| Results stored | opt-in DB | ✗ (POST hook, no receiver) | ✓ | ✓ | ✓ | ✓ | ✓ SQLite |
| Charts over time | ✗ | ✗ | ✓ 24h/week/month | ✓ 24h-30d | ✓ 24h-30d | ✓ | ✗ table of last 50 |
| Min, max, average | ✗ | ✗ | ✓ | averages | ✗ | n/c | ✗ |
| Retention setting | ✗ | ✗ | ✓ days | 30 days fixed, configurable on dev | ✓ days | manual purge | ✗, M3 |
| CSV or JSON export | stability CSV | ✗ | ✓ CSV | dev | ✗ | dev | partial: copy JSON, API |
| **Alerts** | | | | | | | |
| Threshold alerts | stability page only | ✗ | partial: absolute down/up/ping | ✗ | ✗ | ✓ per event | ✗, M3 |
| N-in-a-row, retest, digest | ✗ | ✗ | ✗ (v2 plan) | ✗ | ✗ | ✗ | ✗ |
| Channels | ✗ | ✗ | mail, webhook, Apprise (+7 deprecated) | Discord, Gotify, Healthchecks, Pushover, Telegram, webhook | Healthchecks, webhook, Telegram, Slack, Discord | 26 via Shoutrrr | ✗, M3 plans webhook, ntfy or Apprise |
| **Integrations** | | | | | | | |
| JSON API | partial, test endpoints | ✗ | ✓ tokens, OpenAPI | undocumented | ✓ OpenAPI | cookie session only | ✓ documented v1 (`docs/api.md`) |
| Prometheus | ✗ | ✗ | ✓ v1.11.0 | ✓ undocumented | ✓ | ✗ | ✗, M3 |
| InfluxDB or OpenTelemetry | ✗ | ✗ | ✓ InfluxDB v2 | dev | ✓ OTel | ✗ | ✗ |
| Homepage widget | ✗ | ✗ | ✓ | ✓ | ✓ compat endpoint | ✗ | ✗ |
| Home Assistant | ✗ | ✗ | community | ✗ | ✗ | ✗ | ✗ |
| **Access** | | | | | | | |
| Works with no login | ✓ | ✓ | ✗ ([#1530](https://github.com/alexjustesen/speedtest-tracker/issues/1530)) | ✓ password optional | ✓ reads public | via IP whitelist | ✓ (ADR-0008) |
| Password or local account | stats page | ✗ | ✓ roles | ✓ one password | via OIDC | ✓ | ✗ |
| OIDC or forward-auth | ✗ | ✗ | ✗ (PR open) | ✗ | ✓ OIDC | ✓ OIDC | ✗ |
| Several servers or agents | ✓ test points | partial: lowest-latency pick | partial: random from list | partial: nodes view | ✗ | partial: agents can't test | ✗, M3 node-to-node |
| Share link or image | ✓ PNG | goes to openspeedtest.com | speedtest.net URL | OpenGraph image | ✗ | public dashboard | ✗ (API URL only) |
| **Look and reach** | | | | | | | |
| Light and dark | partial: classic follows system, modern dark only | ✓ | ✓ | ✓ | n/c | ✓ plus paid themes | dark only |
| Translations | n/c | n/c | ✓ Crowdin | ✓ 15 | n/c | ✗ | ✗ |
| Accessibility target | n/c | ✗ ([#113](https://github.com/openspeedtest/Speed-Test/issues/113)) | n/c | n/c | n/c | n/c | ✓ WCAG 2.2 AA (ADR-0015) |
| Native mobile app | Android template | server apps only | third-party iOS | ✗ | ✗ | PWA | ✗, Later (pending #10) |
| CLI test client | ✓ librespeed-cli | ✗ (promised since 2021) | ✗ | ✗ | ✗ | admin commands only | ✗, Later |
| **Deploy** | | | | | | | |
| Single binary | Go and Rust backends | ✗ | ✗ PHP | Linux binaries | image only | ✓ | ✓ |
| Non-root image | ✗ ([#682](https://github.com/librespeed/speedtest/issues/682)) | ✓ | LSIO PUID/PGID | ✗ ([#941](https://github.com/gnmyt/MySpeed/issues/941)) | ✓ | n/c | ✓ `FROM scratch`, 65532 |
| arm64 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ in the Dockerfile; no image published yet |
| Configuration | HTML JS, PHP file, env | edit `index.html`, URL params, env | env plus UI, split | web UI | env only | TOML, env, UI | two env vars; test choices in the browser |
| Third-party calls by default | ipinfo.io | result link to openspeedtest.com | Ookla, icanhazip.com | provider lists, GitHub | Ookla | speedtest.net, LibreSpeed, GitHub update check | none |

What the matrix says. dyno already leads on measurement: it is the only one with server-counted upload, a bufferbloat grade, RPM, a per-test chart and an accessibility target, and it adds no correction factor. It trails every tracker on everything after the test: scheduling, history charts, alerts and integrations. That's M3, and M3 is where the demand is.

## 5. Gaps for dyno, ranked by demand

Requests with real demand that dyno doesn't meet on 2026-10-31. Signal sums reactions and upvotes across the linked threads, counting each thread once. Numbers differ from `market-and-demand.md` where this pass found more threads (Netronome, disc#2304, the MySpeed and HW tables) or split a theme differently.

| Rank | Gap | Signal | Evidence | Who already has it | dyno plan |
|---|---|---|---|---|---|
| 1 | **Scheduled tests with readable history**: chart ranges, min/max/avg, retention, several schedules, failed tests marked on the chart | ~97, 3 projects | [STT#738](https://github.com/alexjustesen/speedtest-tracker/issues/738) 21, [STT disc#650](https://github.com/alexjustesen/speedtest-tracker/discussions/650) 12, [#1244](https://github.com/alexjustesen/speedtest-tracker/issues/1244) 8, [#243](https://github.com/alexjustesen/speedtest-tracker/issues/243) 6, [#1565](https://github.com/alexjustesen/speedtest-tracker/issues/1565) 11, [#509](https://github.com/alexjustesen/speedtest-tracker/issues/509) 5, [#1013](https://github.com/alexjustesen/speedtest-tracker/issues/1013) 5, [HW#816](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/816) 14, [HW#454](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/454) 4, [MySpeed disc#1029](https://github.com/gnmyt/MySpeed/discussions/1029) 4, [disc#883](https://github.com/gnmyt/MySpeed/discussions/883) 4, [#1088](https://github.com/gnmyt/MySpeed/issues/1088) 3 | All four trackers; none marks failed tests on the chart or offers a heat map | M3 covers schedules, min/max/avg and retention. Several schedules, failure markers and the heat map aren't written down |
| 2 | **Alerts that don't nag**: thresholds, N breaches in a row, retest on breach, relative thresholds, daily or weekly digest, more channels | ~84, 4 projects | [STT#328](https://github.com/alexjustesen/speedtest-tracker/issues/328) 15, [#1815](https://github.com/alexjustesen/speedtest-tracker/issues/1815) 9, [#687](https://github.com/alexjustesen/speedtest-tracker/issues/687) 8, [#528](https://github.com/alexjustesen/speedtest-tracker/issues/528) 6, [#908](https://github.com/alexjustesen/speedtest-tracker/issues/908) 3, [#1786](https://github.com/alexjustesen/speedtest-tracker/issues/1786) 4, adaptive intervals in [disc#2304](https://github.com/alexjustesen/speedtest-tracker/discussions/2304) 8, [HW#402](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/402) 10, [HW#339](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/339) 5, [HW#603](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/603)/[#825](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/825) 8, [HW#499](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/499) 4, [MySpeed#776](https://github.com/gnmyt/MySpeed/issues/776) 3, [Netronome#224](https://github.com/autobrr/netronome/issues/224) 0 | Thresholds: STT (absolute only) and Netronome. N-in-a-row, retest and digests: nobody | M3: "alerts through webhook, ntfy or Apprise". The rules are not specified yet |
| 3 | **A test engine that isn't Ookla**, including iperf3-style and own-server tests on a schedule | ~71, 5 projects | [STT disc#976](https://github.com/alexjustesen/speedtest-tracker/discussions/976) 31, [STT disc#2610](https://github.com/alexjustesen/speedtest-tracker/discussions/2610) 14, [STT#64](https://github.com/alexjustesen/speedtest-tracker/issues/64) 9, [MySpeed#1104](https://github.com/gnmyt/MySpeed/issues/1104) 5, [#922](https://github.com/gnmyt/MySpeed/issues/922) 3, [#741](https://github.com/gnmyt/MySpeed/issues/741) 3, [Netronome#180](https://github.com/autobrr/netronome/issues/180) 4, [HW#590](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/590) 2 | Netronome (iperf3, LibreSpeed), MySpeed (LibreSpeed, Cloudflare). STT declined a second engine in Aug 2026 ([PR#2862](https://github.com/alexjustesen/speedtest-tracker/pull/2862)) | M3 node-to-node, with a second dyno as the far end. That meets the "own server" and LAN cases. It doesn't meet "test my ISP against the internet" unless the user runs a dyno on a VPS; pending #4 already says so |
| 4 | **Several vantage points**: more than one far end, a series per server, agents that run tests | ~45, 5 projects | [MySpeed#945](https://github.com/gnmyt/MySpeed/issues/945) 7, [HW#806](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/806) 9, [STT#1877](https://github.com/alexjustesen/speedtest-tracker/issues/1877) 5, [#1452](https://github.com/alexjustesen/speedtest-tracker/issues/1452) 4, [#2586](https://github.com/alexjustesen/speedtest-tracker/issues/2586) 3, [disc#1602](https://github.com/alexjustesen/speedtest-tracker/discussions/1602) 6, [Netronome#98](https://github.com/autobrr/netronome/issues/98) 3, [#197](https://github.com/autobrr/netronome/issues/197) 4, [disc#192](https://github.com/autobrr/netronome/discussions/192) 2, [OST#148](https://github.com/openspeedtest/Speed-Test/issues/148) 1 | Partly: LibreSpeed test points, MySpeed's nodes view. Nobody has agents that run tests | M3 node-to-node; "multi-node fleet view" is Later |
| 5 | **Prometheus and other exports** | ~37, 3 projects | [STT#53](https://github.com/alexjustesen/speedtest-tracker/issues/53) 15, [STT disc#24](https://github.com/alexjustesen/speedtest-tracker/discussions/24) 9, [HW#658](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/658) 6, [MySpeed#1087](https://github.com/gnmyt/MySpeed/issues/1087) 2, [disc#1009](https://github.com/gnmyt/MySpeed/discussions/1009) 2, [disc#1465](https://github.com/gnmyt/MySpeed/discussions/1465) 3 | STT, HW v2 and MySpeed all ship `/metrics` now; it has become table stakes since 2026-10-30's file listed it as unmet | M3 `/metrics`. InfluxDB and OpenTelemetry aren't planned |
| 6 | **Auth beyond none**: OIDC, a trusted forward-auth header, a simple password | ~30, 2 projects | [STT PR#2801](https://github.com/alexjustesen/speedtest-tracker/pull/2801) 8, [disc#391](https://github.com/alexjustesen/speedtest-tracker/discussions/391) 5, [#620](https://github.com/alexjustesen/speedtest-tracker/issues/620) 6, [#1412](https://github.com/alexjustesen/speedtest-tracker/issues/1412) 4, [#336](https://github.com/alexjustesen/speedtest-tracker/issues/336) 3, [MySpeed#767](https://github.com/gnmyt/MySpeed/issues/767) 4 | HW v2 and Netronome have OIDC | Not planned. ADR-0008 already meets the larger ask, which is no login at all ([STT#1530](https://github.com/alexjustesen/speedtest-tracker/issues/1530), 39). It keeps the door open for a header or token later |
| 7 | **Dashboard widgets**: Homepage, Home Assistant, MQTT | ~27, 2 projects | [STT#81](https://github.com/alexjustesen/speedtest-tracker/issues/81) 15 (41 comments), [STT disc#1991](https://github.com/alexjustesen/speedtest-tracker/discussions/1991) 7, [MySpeed#614](https://github.com/gnmyt/MySpeed/issues/614) 3, [#936](https://github.com/gnmyt/MySpeed/issues/936) 1, [#1190](https://github.com/gnmyt/MySpeed/issues/1190) 1 | Homepage: STT, MySpeed, HW. Home Assistant: nobody natively | Not planned. Homepage support is a PR to [gethomepage](https://github.com/gethomepage/homepage/pull/3662) reading dyno's existing `/api/v1/results` |
| 8 | **Continuous ping or stability mode** | ~25, 3 projects | [STT disc#2219](https://github.com/alexjustesen/speedtest-tracker/discussions/2219) 14, [LS#479](https://github.com/librespeed/speedtest/issues/479) 8, [HW#928](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/928) 3 | LibreSpeed `stability.html` (since 2026-07), Netronome monitors | Not planned. `market-and-demand.md` §5 lists it behind disclosure |
| 9 | **Packet loss** | ~14, 3 projects | [STT#2155](https://github.com/alexjustesen/speedtest-tracker/issues/2155) 7, [MySpeed#1045](https://github.com/gnmyt/MySpeed/issues/1045) 4, [#922](https://github.com/gnmyt/MySpeed/issues/922) 3 | STT (from Ookla), Netronome (MTR) | Later, over WebTransport (pending #5) |
| 10 | **Live progress for scheduled tests** | ~11, 2 projects | [STT#2035](https://github.com/alexjustesen/speedtest-tracker/issues/2035) 9, [OST#145](https://github.com/openspeedtest/Speed-Test/issues/145) 2 | Netronome | Not planned. dyno owns its engine, so this is possible where the Ookla trackers say it isn't |

Smaller gaps, under 10 signal each:

- **Client IP and IPv4 against IPv6** ([OST#160](https://github.com/openspeedtest/Speed-Test/issues/160) 4, [STT#615](https://github.com/alexjustesen/speedtest-tracker/issues/615) 6, [homebox#42](https://github.com/XGHeaven/homebox/issues/42)). Not planned.
- **ISP evidence**: long-period totals, SLA reports, export ([MySpeed#1088](https://github.com/gnmyt/MySpeed/issues/1088) 3, disc#2304 SLA reply 3). docsight's 290 stars in nine months suggest more demand than the votes show. Not planned for the core; `market-and-demand.md` puts per-site SLA reports in the business column.
- **CLI client.** Few votes ([OST#2/#36/#54/#83](https://github.com/openspeedtest/Speed-Test/issues/83), 3 reactions, 16 comments) but real use: librespeed-cli 853 stars, cloudflare-speed-cli 1,082 stars in ten months, and [LS-cli#104](https://github.com/librespeed/speedtest-cli/issues/104) praising an open CLI because "Speedtest bans 'commercial' use". Later in the roadmap.
- **Pause tests during other heavy traffic** (disc#2304 reply, 6; [Netronome#188](https://github.com/autobrr/netronome/issues/188)). Not planned.
- **Light theme** ([LS#440](https://github.com/librespeed/speedtest/issues/440) asked for dark, [MySpeed#795](https://github.com/gnmyt/MySpeed/issues/795) for light, 3). dyno is dark only. Not planned.
- **iOS app** ([LS#256](https://github.com/librespeed/speedtest/issues/256) 5, 14 comments). Later (pending #10).

Requests dyno already meets, so the docs and README can say so:

- Latency under load and a bufferbloat grade (about 28 signal over four projects in `market-and-demand.md`; also LS PR#562 RPM). No self-hosted competitor grades it. STT closed [#2262](https://github.com/alexjustesen/speedtest-tracker/issues/2262) without adding one.
- A UI people like (about 68 in `market-and-demand.md`, mostly LibreSpeed).
- No forced login ([STT#1530](https://github.com/alexjustesen/speedtest-tracker/issues/1530), 39).
- Single or multi-stream choice ([LS#697](https://github.com/librespeed/speedtest/issues/697), 4), a restart button ([OST#140](https://github.com/openspeedtest/Speed-Test/issues/140), 5), keyboard access ([OST#113](https://github.com/openspeedtest/Speed-Test/issues/113)), units ([HW#788](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/788)).
- Results kept on the box, not sent to a vendor ([OST#55](https://github.com/openspeedtest/Speed-Test/issues/55)).
- A documented JSON API (STT has one; MySpeed's and Netronome's are undocumented or session-only).
- A non-root image ([LS#682](https://github.com/librespeed/speedtest/issues/682), [MySpeed#941](https://github.com/gnmyt/MySpeed/issues/941)).

## 6. What users dislike, so dyno should avoid it

- **Correction factors on by default.** LibreSpeed multiplies by 1.06, OpenSpeedTest by 1.04. Both are documented, neither is shown on the result.
- **Upload counted in the browser and zero-filled payloads.** Every "impossible upload" thread across LibreSpeed, OpenSpeedTest and homebox comes from this (section 3). dyno already avoids both; keep it that way.
- **Config spread across files and the UI.** LibreSpeed has three surfaces, OpenSpeedTest wants you to edit `index.html`, STT splits env vars and UI and its threshold env vars only apply on first setup. STT users call restart-to-change-schedule "error-prone and causes downtime" ([disc#2304](https://github.com/alexjustesen/speedtest-tracker/discussions/2304)). HW v2 moved everything to env vars and a returning user was lost ([HW#1188](https://github.com/henrywhitaker3/Speedtest-Tracker/issues/1188)). Pick one place for each setting and say which.
- **Forced login with a default password** (STT), and password recovery that needs SQL (MySpeed, HW v1).
- **Alerts that fire on every test** rather than on a breach ([MySpeed#776 comment](https://github.com/gnmyt/MySpeed/issues/776)). Notifications that go missing or arrive twice ([STT#2332](https://github.com/alexjustesen/speedtest-tracker/issues/2332)).
- **A scheduler that dies silently** ([STT#1065](https://github.com/alexjustesen/speedtest-tracker/issues/1065), 79 comments). A tracker needs to say when it last ran and alert when it stops.
- **Pre-flight checks that depend on outside services** (STT's ping and icanhazip check, [#2560](https://github.com/alexjustesen/speedtest-tracker/issues/2560)).
- **Results that leave the box** (OpenSpeedTest's result link) and **a known TLS private key shipped in an image** (OpenSpeedTest `files/nginx.key`).
- **One chart range picker per chart** ("comparing always make me change 6 drop down boxes", disc#2304) and charts that hide failed runs ([STT#509](https://github.com/alexjustesen/speedtest-tracker/issues/509)).
- **Hard history caps** (MySpeed's 30 days in every release since 2024).
- **Long gaps between releases while `development` moves on** (MySpeed), stale bots closing endorsed requests (LibreSpeed), mass closure without comment (HW), and promises that don't ship (OpenSpeedTest's CLI since 2021). These cost trust more than any missing feature.
- **Features that exist but aren't documented** (MySpeed's Prometheus endpoint and API).
- **Profanity in the product** ([LS#473](https://github.com/librespeed/speedtest/issues/473), 14 reactions, the most-reacted LibreSpeed issue that isn't a request).
- **Paywalling looks.** Netronome sells themes and keeps the premium CSS out of the source. No complaints yet, but it is a precedent to weigh against the licence question (pending #6).

## 7. What changed in 2025-2026

- **henrywhitaker3 rewrote the tracker in Go in a week.** v2.0.0 to v2.7.2 shipped between 2026-10-22 and 2026-10-29: Go and SQLite, OIDC, Prometheus, OpenTelemetry, OpenAPI, distroless non-root image. Architecturally it is the closest project to dyno, and it is still Ookla-only with no browser test and no thresholds.
- **Netronome reached 1.4k stars in 16 months** with three engines, MTR packet loss, DNS monitors, 26 alert channels, OIDC and Tailscale agents. It has no browser test and no loaded latency, and its agents can't run tests yet ([#98](https://github.com/autobrr/netronome/issues/98)). It sells cosmetic themes.
- **NetworkOptimizer reached 1.2k stars in ten months** and is the only product that combines browser, LAN and WAN tests with loaded latency and scheduled alerts. It is UniFi-only and BSL with a paid tier.
- **speedtest-tracker added Prometheus (v1.11.0, 2025-12) and Apprise (v1.12.0)**, then froze v1 in July 2026. It declined a non-Ookla engine in August 2026 ([PR#2862](https://github.com/alexjustesen/speedtest-tracker/pull/2862)) and has a headless multi-engine service on its v2 plan that hasn't shipped.
- **LibreSpeed shipped a modern UI (v6.0.0, 2026-04), a stability page (v6.2.0, 2026-07), a PWA and a new share image (v6.3.0).** Its loaded-latency and RPM requests were stale-closed in the same period.
- **MySpeed's development branch** gained unlimited retention, InfluxDB, ntfy, CSV export and a Bun runtime in 2026, none of it released.
- **OpenSpeedTest's app code hasn't changed since 2024.** The author's 2026 work went into OpenPacketLoss.
- **AI-assisted maintenance is now common**: LibreSpeed PRs "with @Copilot", Netronome's AI triage, `AGENTS.md` in homebox and Netronome. Maintainer response times on new issues have improved where this happened.
