# ADR 0025: Web analytics are provider adapters that report visits and signups by Araldo's own link tags

- Status: accepted; built for Plausible and GA4
- Date: 2026-10-03

## Context

Araldo reports what it can see: engagement on posts
([ADR 0018](0018-engagement.md)), spend on ads
([ADR 0023](0023-paid-promotion.md)), opens and clicks on newsletters
([ADR 0024](0024-newsletters.md)). What a brand wants to know is what
each of those brought: visits, and the signups or purchases that followed.
That happens on the brand's own site, in its web analytics, which Araldo
has kept out of on purpose ([ADR 0016](0016-link-tagging.md)): no
tracking pixel, no tracking script.

Araldo already writes the tags that answer the question. Every link it
publishes carries UTM parameters it chose, and its campaign and content
values name the post, promotion or issue. The analytics tool records them
with each visit. All that is missing is reading the counts back.

The suites that close this loop (HubSpot most of all) do it by owning a
CRM and a tracking script. Brands use many analytics tools: privacy-first
ones (Plausible, Fathom, Umami, Matomo), Google Analytics 4, product
analytics (PostHog). Each has a reporting API.

## Decision

1. **Analytics are adapters**, one package each under `internal/analytics/`,
   like platforms, ad networks and mail providers. Plausible is first, then
   Google Analytics 4; Umami, Matomo, PostHog and Fathom follow when someone
   needs them. A sandbox source in test mode invents stable numbers.
2. **A brand connects any number of analytics sources**
   (`analytics_sources`): the provider, its site or property, its
   credentials encrypted ([ADR 0008](0008-encryption.md)), and the goals
   that count as conversions (Plausible goal names, GA4 key events).
   Connecting needs `brands:write`.
3. **Adapters report counts, never visitors**: for a date range, visits
   and each goal's completions grouped by `utm_source`, `utm_medium`,
   `utm_campaign` and `utm_content`. No visitor, session or address ever
   reaches Araldo, whatever the tool could return.
4. **Araldo matches the rows to its own work** through the tags it wrote:
   `utm_content` names the post, promotion or issue and `utm_campaign`
   its campaign. Rows carrying tags Araldo did not write stay
   unattributed and are shown as such, so a brand sees how much of its
   traffic the matching explains. (A newsletter issue is named by
   `utm_campaign`, and `utm_content` gives the link's place in it, as
   [ADR 0024](0024-newsletters.md) decision 10 says.)
5. **Readings follow a schedule**: daily, re-reading the last 7 days, since
   tools revise recent days (GA4 most of all, which can take a day or two
   and hides small counts). Recent figures are marked provisional.
6. **Results sit next to cost**: signups and cost per signup by post,
   channel, template, ad campaign and newsletter issue, in the API
   (`/v1/analytics/summary`), the dashboard's Performance and Ads pages,
   client reports, and MCP.
7. **The tagging rules are one place**: what Araldo writes in each UTM
   field for posts, promotions and issues is defined once and used by the
   link tagger and by the matcher, so they cannot drift apart.

## Alternatives considered

- **A tracking script of Araldo's own.** It would see everything, and make
  Araldo the kind of tracker it refuses to be; brands already run
  analytics.
- **Webhooks from the site** ("a signup happened, from this tag"). Exact,
  but every brand would have to wire it up; reading the analytics they
  already have needs nothing on their side.
- **Reading visitor-level exports** to deduplicate across tools. More
  precise, and personal data Araldo would then have to protect.

## Consequences

- Attribution is only as good as the tags: a link typed by hand or a site
  that strips parameters shows up as unattributed, and the dashboard says
  how much.
- New tables (`analytics_sources`, readings), a package per provider, a
  task, and a summary endpoint.
- Switching analytics tools is cheap, as with every other adapter: the
  tags and the history stay in Araldo.
