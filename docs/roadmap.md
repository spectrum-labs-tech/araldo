# Roadmap

What Araldo is for, what is done, what is decided but not built, and the
order it is being built in. The ADRs carry each decision's reasons; this
file keeps the order and the status in one place. Update it in the same
change as the work.

## What Araldo is for

The publishing layer products and AI agents can trust: it checks every post
against each platform's rules before sending, never posts twice, lets a
person approve what matters, and puts a brand's social posts, ad spend,
newsletters and the signups they bring in one place, for many brands and
organizations. Self-hosted under the AGPL, or hosted.

Principles every feature keeps:

- **Adapters everywhere.** Platforms, ad networks, mail providers and web
  analytics are replaceable adapters; the work (content, templates,
  approvals, history, results) stays in Araldo, so switching a provider is
  cheap.
- **Trust in automation.** Validate before acting, at most once, a person
  gates what matters, test mode for everything.
- **No audience data.** No tracking pixel, no subscriber lists, no visitor
  records: counts from the tools that own them.
- **API first, then the dashboard, then MCP**, all on the same contract.

## Done

- Tenancy: orgs, brands, members with four roles, optional approvals, audit
  log.
- Sign-in: argon2id passwords, TOTP with QR enrollment, recovery codes,
  sessions, re-authentication for sensitive actions, org-wide MFA
  requirement.
- API: `/v1` with API keys (test and live, scoped, brand-limited),
  idempotency keys, cursor pagination, problem details, rate limits; the
  OpenAPI contract with a test that keeps it in step with the routes.
- Templates with JSON Schema, per-platform bodies and fit modes; previews
  with per-platform violations.
- Publishing: slots taken at approval, moving and swapping posts, deadlines,
  rate-limit holds, re-auth detection, never silently double-posting
  ([ADR 0011](adr/0011-publishing.md), [ADR 0022](adr/0022-slots-at-approval.md)).
- Platforms: Bluesky, Mastodon, Gab, X, LinkedIn (members and Pages) and Pinterest (signing in
  through an org's developer app, or pasted credentials), Threads, Facebook
  Pages and Instagram (signing in), Discord and Telegram, and the sandbox with failure
  simulation ([ADR 0021](adr/0021-oauth-connections.md)).
- Events and signed webhooks with retries, a delivery log and resend.
- UTM tagging of links to a brand's own sites; Bluesky posts show short
  links ([ADR 0016](adr/0016-link-tagging.md)).
- Images on posts: uploads or URLs, checked against each platform's limits,
  stored in Postgres or S3-compatible storage ([ADR 0017](adr/0017-media.md)).
- Engagement read on a schedule, with a summary by post, channel and
  template and the dashboard's Performance page ([ADR 0018](adr/0018-engagement.md)).
- Administration by API behind explicit-only scopes ([ADR 0019](adr/0019-administration-api.md)).
- An MCP server over stdio (`araldo mcp`, [ADR 0020](adr/0020-mcp.md)).
- Ads phase 1: ad accounts connected by sign-in, Reddit Ads first, with
  spend and results by brand, account, campaign and day
  ([ADR 0023](adr/0023-paid-promotion.md)).
- Web analytics read from Plausible and GA4: signups by post, network and
  campaign, and cost per signup beside ad spend
  ([ADR 0025](adr/0025-web-analytics.md)).
- Newsletters phase 1: mail accounts (Brevo first), issues with approval,
  a delivery per account handed to its provider a day ahead, email-safe
  rendering with a brand theme, test sends and results
  ([ADR 0024](adr/0024-newsletters.md)).
- Video (streamed through S3-compatible storage, never transcoded) on every
  platform that takes it, YouTube and TikTok among them, and images resized
  for each platform that needs it ([ADR 0027](adr/0027-video-and-resizing.md)).
- Brand reports: a month beside the one before, across publishing,
  engagement, web traffic, ads and newsletters, on a printable page, in
  the API and over MCP ([ADR 0026](adr/0026-reports.md)).
- MCP over HTTP at `POST /v1/mcp`, and a daily credential check per
  channel.
- Dashboard (WCAG 2.2 AA, with guides beside pages that need setup
  elsewhere); one binary; Helm chart; CI publishing to GHCR; metrics
  through OpenTelemetry with the chart's optional PodMonitor and alerts.

## Order of work

| # | Work | Decision | Status |
| --- | --- | --- | --- |
| 1 | Prove the adapters with real accounts on every network; a daily credential check per channel; a daily test post on dedicated test accounts so a platform change is caught early | ADR 0011 | Adapters and the daily check built; real-account runs and test posts not yet |
| 2 | MCP over HTTP at `POST /v1/mcp` | ADR 0020 | Done |
| 3 | Web analytics adapters: signups by post, network and campaign (Plausible), then cost per signup beside ad spend and GA4 | ADR 0025 | Done: Plausible and GA4, signups by post, cost per signup by ad campaign |
| 4 | Newsletters phase 1: mail accounts, issues with a delivery per account, renderer and theme, Brevo and sandbox providers | ADR 0024 | Done: Brevo and the sandbox, approval, hand-off, results, dashboard, API and MCP drafting; templates with data and a top-posts block are phase 2 |
| 5 | Client reports: one page per brand per month across posts, ads, newsletters and analytics | ADR 0026 | Phase 1 done: the page with a print stylesheet, the API and the MCP tool; share links next |
| 6 | Pinterest (images) | ADR 0009 | Done: boards as channels, by sign-in or pasted token; engagement reading next |
| 7 | Video media and resizing images to fit each platform, then YouTube and TikTok | ADR 0027 | Done: images resized per platform; video on Bluesky, X, Mastodon, Gab, LinkedIn, Facebook, Instagram (reels), Threads, Telegram, Discord, YouTube and TikTok |
| 8 | Ads phase 2: promotions on the first network with real spend, under per-brand caps | ADR 0023 | Decided |
| 9 | LinkedIn company pages (its Community Management API); engagement from X and LinkedIn | ADR 0018, 0021 | Pages built, as their own platform with their own app (LinkedIn grants the API only to an app with no other products), not yet run against a real Page; engagement not yet |
| 10 | Developer tooling: the CLI as an API client with `araldo login` (device sign-in), `araldo listen` (webhooks to localhost), SDKs generated from the contract, a request log in the dashboard | ADR 0005, 0028 | CLI client built: device sign-in (`araldo auth login`) with user tokens that act as the person, a test and a live credential per server (`--live`, as the Stripe CLI), `--org`, `members`, `org`, `channels list`, `araldo api`, `araldo listen` (the event stream forwarded to localhost) and `GET /v1/me`; server administration under `araldo admin`, audited as the operator. SDKs and a request log next |
| 11 | AI drafting in the dashboard with the organization's own model key | ADR 0001 | Planned |
| 12 | The rest of OpenTelemetry (traces, logs, publish lateness) | ADR 0014 | Planned (built-in backups, ADR 0013, were withdrawn: they are the operator's) |
| 13 | Passkeys (WebAuthn) and OIDC single sign-on | ADR 0007 | Planned |
| 14 | Hosted plan: a separate install with sign-up, billing and developer apps shared by every organization, at araldo.dev (the project's site and docs, `app.araldo.dev` for the dashboard, `api.araldo.dev` for the API) | ADR 0021, 0030 | Install-wide developer apps built (`araldo admin apps`); sign-up and billing not yet |

## Not now

- **A social inbox** (reading and answering comments and DMs): large,
  crowded, and outside what sets Araldo apart ([ADR 0001](adr/0001-scope.md)).
  If customers ask, it starts read-only from the engagement readers.
- **Sending email or holding subscriber lists**: providers do that
  ([ADR 0024](adr/0024-newsletters.md)).
- **A tracking script** ([ADR 0025](adr/0025-web-analytics.md)).
