# Roadmap

What Araldo is for, what is decided but not built, and the order it is
being built in. The ADRs carry each decision's reasons; this file keeps the
order and the status in one place. Update it in the same change as the work.

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

## Order of work

| # | Work | Decision | Status |
| --- | --- | --- | --- |
| 1 | Prove the adapters with real accounts on every network, and a daily test post per network so a platform change is caught early | ADR 0011 | Adapters built; real-account runs and test posts not yet |
| 2 | MCP over HTTP at `POST /v1/mcp` | ADR 0020 | Decided |
| 3 | Web analytics adapters: signups and cost per signup by post, ad and issue (Plausible, then GA4) | ADR 0025 | Decided |
| 4 | Newsletters phase 1: mail accounts, issues with a delivery per account, renderer and theme, Brevo and sandbox providers | ADR 0024 | Decided |
| 5 | Client reports: one page per brand per month across posts, ads, newsletters and analytics | ADR 0024, 0025 | Planned |
| 6 | Pinterest (images) | ADR 0009 | Planned |
| 7 | Video media, then YouTube and TikTok | ADR 0009, 0017 | Planned |
| 8 | Ads phase 2: promotions on the first network with real spend, under per-brand caps | ADR 0023 | Decided |
| 9 | AI drafting in the dashboard with the organization's own model key | ADR 0001 | Planned |
| 10 | Hosted plan: a separate install with sign-up, billing and approved developer apps shared by every organization | ADR 0021 | Planned, after 1 to 7 |

## Built

- Posts across Bluesky, Mastodon, Gab, X, LinkedIn, Threads, Facebook
  Pages, Instagram, Discord and Telegram, with per-platform rules and
  preview (ADR 0009, 0010).
- Approval, slots taken at approval, moving and swapping posts (ADR 0004,
  0022).
- Images (ADR 0017), engagement (ADR 0018), the administration API
  (ADR 0019), MCP over stdio (ADR 0020), OAuth connections (ADR 0021).
- Ads phase 1: ad accounts and their results, Reddit Ads first (ADR 0023).

## Not now

- **A social inbox** (reading and answering comments and DMs): large,
  crowded, and outside what sets Araldo apart (ADR 0001). If customers ask,
  it starts read-only from the engagement readers.
- **Sending email or holding subscriber lists** (ADR 0024).
- **A tracking script** (ADR 0025).
