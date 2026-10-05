# ADR 0026: A brand's report is computed on demand from what Araldo already reads, one period at a time

- Status: accepted; phase 1 and share links built; monthly email not yet
- Date: 2026-10-03

## Context

A brand's results are spread over four pages: engagement on posts, web
analytics (signups by post, network and campaign, [ADR 0025](0025-web-analytics.md)),
ad spend ([ADR 0023](0023-paid-promotion.md)) and newsletter results
([ADR 0024](0024-newsletters.md)). An owner reviewing the month, a team
lead preparing a meeting, or an agency reporting to a client wants one
page that answers "what did we publish, what came of it, and what did it
cost", compared with the period before. Every scheduling tool sold to
agencies has one, usually as a PDF.

Araldo already stores every number such a page needs. What it lacks is the
page.

## Decision

1. **A report is computed when asked for**, for one brand and one period,
   from the readings Araldo already keeps. Nothing about a report is
   stored, so it always shows the latest numbers; days that sources still
   revise (the last week of analytics and ads) are marked as such.
2. **A period is a calendar month in the brand's time zone** by default,
   or any range of up to a year. Each figure is shown beside the same
   figure for the period of equal length just before, with the change.
3. **Sections**, each left out when the brand has nothing in it:
   - *Publishing*: posts published and failed, by network.
   - *Engagement*: totals, the top posts, and by channel.
   - *Web traffic*: visitors and signups, the untagged share, and the top
     sources, campaigns (newsletter issues by subject) and posts.
   - *Ads*: spend, clicks, signups and cost per signup, by campaign.
   - *Newsletters*: issues sent, delivered, clicks and click rate,
     unsubscribes, by issue.
4. **Amounts keep their currency.** Ad accounts in different currencies
   are totaled separately, never converted.
5. **A report shows what the reader may see**: it needs `posts:read`, the
   ads section `ads:read` and the newsletters section `newsletters:read`;
   a key limited to a brand gets that brand only.
6. **Surfaces**: the dashboard's Reports page, with a print stylesheet so
   the browser saves a clean PDF (Araldo renders no PDFs itself);
   `GET /v1/reports` in the API; and an MCP tool, so an agent can write
   the month's summary from it.

### Phases

- **Phase 1:** the computed report, the page with its print stylesheet,
  the API and the MCP tool.
- **Phase 2:** sharing a report outside the org: a revocable, expiring link
  to a read-only page without sign-in, in the brand's email theme.
- **Phase 3:** sending the report each month to chosen people.

## Alternatives considered

- **Storing monthly snapshots.** Stable figures to quote, but they go stale
  as sources revise late conversions, and add tables and a task for
  numbers that can be computed in milliseconds.
- **Generating PDFs on the server.** A rendering engine in the binary for
  what every browser's "Save as PDF" does from a print stylesheet.
- **Converting currencies.** Needs exchange rates from a third party, and
  hides what was actually spent.

## Consequences

- Reports cost nothing to keep and are never out of date; two people
  printing the same month a week apart can see different recent numbers,
  which the page says.
- Each new kind of result (video views, a new ad network) is a section to
  add here too.
- Sharing with people outside the org is a separate, later decision about
  links that work without signing in.
