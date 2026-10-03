# ADR 0023: Paid promotion on any network, with spend that cannot exceed a cap

- Status: proposed
- Date: 2026-10-02

## Context

[ADR 0001](0001-scope.md) left ads out of scope. Two things changed:

- Organic reach is not always enough. Putting a small, capped budget behind
  what already works is how most small products get traffic.
- Araldo already knows which posts work ([ADR 0018](0018-engagement.md)),
  and a place that shows paid results next to organic ones, across brands
  and networks, is useful on its own.

The first real use is a developer product in a closed alpha. Its audience
is on Reddit (communities about open models and self-hosting) and in
search ("OpenAI batch API alternative"), not on Facebook. So the networks
cannot be fixed in advance, and they do not all work the same way:

- **Some promote an existing post** (Meta, X, LinkedIn Page posts, TikTok
  Spark Ads), which fits posts Araldo published. **Others run ads that are
  not posts** (Reddit promoted posts written for the ad, Google search
  ads). Both are needed.
- **Each network's ads API needs its own approval**: Google's developer
  token, Reddit's and LinkedIn's ads API applications, Meta's Marketing API
  access tiers. Approval takes days to weeks. A hosted Araldo would hold
  one approved app per network; a self-hosted install brings its own, as
  with posting ([ADR 0021](0021-oauth-connections.md)).
- **Ads spend money.** A bug in posting publishes a bad post; a bug in ads
  spends someone's budget. Every rule about money below exists to bound
  that.
- **Every network reviews ads** and can reject them (firearms and their
  parts, for example, cannot be advertised on most), so a promotion can
  fail after Araldo has done everything right.
- **Measurement pixels conflict with privacy.** Ad networks optimize best
  with their tracking pixel on the advertiser's site, but a pixel means
  cookies, a consent banner and sharing visitors with the network. Products
  that sell privacy cannot use one.

## Decision

### Scope

1. **Araldo runs small, bounded promotions; it is not an ads manager.** A
   *promotion* is one of two kinds:
   - a **boost**: a budget behind a post Araldo published, where the
     network can promote an existing post;
   - a **campaign**: one ad (text, a link and optionally images from
     `/v1/media`) with a budget and an end, where the ad is not a post.

   Each promotion maps to the smallest structure the network needs (on
   Meta one campaign, ad set and ad; on Google one campaign, ad group and
   ad), which Araldo creates and owns. No audience builder, no bidding
   strategies, no editing of structures Araldo did not create.
2. **Ad text goes through the rules engine.** Networks limit ad text the
   way they limit posts (a Google headline is 30 characters, a description
   90). Each network's limits are rules with their sources
   ([ADR 0009](0009-platform-adapters.md)), so a preview lists every
   violation before anything is created, as for posts.
3. **Reporting covers whole ad accounts**: spend and results for every
   campaign in a connected account, including ones made in the network's
   own tools, by brand. One page answers "what are my ads doing" across
   brands and networks.

### Networks

4. **Adapters per network**, under `internal/ads/`, with two optional
   interfaces: `Reporter` (read accounts, campaigns and results) and
   `Promoter` (create, pause, resume and end a promotion, saying which
   kinds it supports). The core (accounts, promotions, caps, the outbox,
   readings) is the same for all.
5. **A network gets reporting when someone runs ads there, and promotions
   when someone will spend through Araldo there.** The order follows real
   spend, not a plan; the first adapter is the network the first real
   budget goes to. Reporting can come first because read access is easier
   to get approved and cannot cost anything.

### Accounts and access

6. **An ad account belongs to a brand** (`ad_accounts`): network, the
   network's account ID, name, currency and time zone as the network
   reports them, and credentials encrypted like a channel's
   ([ADR 0008](0008-encryption.md)). It connects through an org's developer
   app for that network's ads API, a separate provider from posting
   (`meta_ads`, `reddit_ads`, `google_ads`…): ads access brings its own
   review, and trouble there must not touch posting.
7. **Starting spend is an explicit-only power**
   ([ADR 0019](0019-administration-api.md)): `ads:write` is never in a
   key's default scopes, and only admins and owners hold it. `ads:read` is
   an ordinary scope.

### Money

8. **Every promotion has a lifetime budget and an end time**, never a daily
   budget and never open-ended; the longest is 30 days. Where a network
   only takes daily budgets, Araldo sets the daily budget to the lifetime
   budget divided by the days and counts the network's allowed daily
   overspend against the cap. Amounts are integers in the account
   currency's minor units.
9. **Each brand has a monthly cap per currency** (default zero, so nothing
   spends until someone sets one). A promotion commits its whole budget,
   plus any allowed overspend, to the month it starts in. Creating one
   checks in the same transaction that committed plus new stays within the
   cap; otherwise `409 ad_cap_exceeded`. The commitment is the worst case,
   so the cap holds even if every promotion spends in full.
10. **The network's own account limit is a backstop**, not the guard:
    Araldo shows it where the network has one, and recommends setting it.
11. **Nothing goes live half-built.** A promotion is an outbox row, like a
    target ([ADR 0011](0011-publishing.md)). The worker creates every part
    **paused**, records each network ID as it goes, and only then turns it
    on. Creation calls are not idempotent on most networks, so a call
    whose result is unknown goes to `needs_attention`, never retried
    blindly; a later attempt resumes from the last recorded ID.
12. **Stopping always works.** Pausing or ending a promotion, or every
    promotion of a brand at once, needs only `posts:write`: anyone who can
    post can stop spending; only `ads:write` can start it.
13. **Test mode spends nothing.** It runs against the sandbox adapter, with
    invented results. Networks' own test accounts (Google Ads test
    accounts, Meta sandbox ad accounts) can be connected in live mode to
    check the real calls without delivery.

### Measurement

14. **No pixels.** Araldo never asks for a network's tracking pixel or
    conversion API. Landing links are tagged with UTM parameters
    ([ADR 0016](0016-link-tagging.md)) using `utm_medium=paid` and the
    promotion's ID as `utm_content`, so the brand's own, privacy-friendly
    analytics measure signups. Promotions optimize for clicks or reach,
    which networks measure on their side.
15. **Results are read on a schedule**, like engagement: hourly while
    running, then daily for 7 days after the end (networks attribute
    late), then stop. Each reading keeps spend, impressions, reach where
    reported, clicks and the network's result count. A boost shows its
    paid results next to the organic engagement of the same post.

### Lifecycle

16. **A promotion's status follows the network's**: `pending` (being
    built), `in_review`, `active`, `rejected` (with the network's reason),
    `paused`, `completed`, `canceled`, `needs_attention`. Each change is an
    event (`promotion.*`, [ADR 0012](0012-events-and-webhooks.md)) and
    each action an audit entry. A rejection is a normal outcome, reported
    with its reason, not an error.
17. **Targeting is the network's simplest useful form, and only that**:
    locations everywhere, plus the one thing that makes each network
    worth using (communities on Reddit, keywords on Google search, job
    functions on LinkedIn, the automatic audience on Meta). A brand
    declares whether its ads fall in a special category (housing,
    employment, credit, politics) where networks require it.

### Surfaces

18. **API**: `/v1/ad_accounts`, `/v1/promotions` (preview, create, list,
    read, pause, resume, cancel) and `/v1/ads/summary` (spend and results
    by brand, network, account or promotion). Contract first, as always
    ([ADR 0005](0005-api-conventions.md)).
19. **Dashboard**: an Ads page per brand (accounts, cap, spend this month,
    promotions, results), and "Promote" on a published post where a
    connected network can boost it.
20. **MCP is read-only for ads at first**: an agent can report on spend and
    suggest what to promote, but not spend.

### Phases

- **Phase 1:** the core, the sandbox adapter and reporting for the first
  network with real spend. Nothing spends through Araldo.
- **Phase 2:** campaigns on that network, with caps and the outbox.
- **Phase 3:** boosts, on the first network that can promote a post
  Araldo published.
- **Later:** each further network when someone spends there.

## Alternatives considered

- **A separate ads app.** It would rebuild orgs, brands, members, keys,
  developer apps, the audit log and engagement, and the two would still
  need each other's data. A module with a hard boundary (its own package,
  scopes and provider apps) keeps the separation without the duplication.
- **Meta first, boosts only.** The first real buyer's audience is not on
  Meta, and the best-fitting networks for it (Reddit, search) do not
  promote existing posts.
- **A full ads manager** (campaign trees, audiences, bidding). A different
  product with many competitors and the networks' own free tools.
- **Daily budgets.** They can overspend on a given day and have no end; a
  lifetime budget with an end time bounds spend.
- **Relying on the network's account spending limit.** It is per account,
  shared with campaigns Araldo does not manage, and easy to leave unset.
- **Conversion pixels for better optimization.** They trade the visitors'
  privacy, and for some brands their whole pitch, for a better bid.
- **Letting agents spend through MCP.** Possible later with `ads:write`,
  but the first version should not let a misread prompt buy ads.

## Consequences

- [ADR 0001](0001-scope.md)'s "ads" exclusion narrows to everything beyond
  small, bounded promotions and reporting on ad accounts.
- New tables (`ad_accounts`, `promotions`, readings), a package per
  network, provider apps per ads API, scopes (`ads:read`, `ads:write`),
  events and tasks.
- Spend is bounded three ways: lifetime budgets with end times, a monthly
  cap per brand that counts every promotion at its worst case, and the
  network's own limit as a backstop.
- Ads measured without pixels optimize less well than ads with them; the
  brand's own analytics, not the network, say which ads brought signups.
- Each network needs its own API approval before Araldo can use it, so the
  first weeks of any network's ads run in its own tools, with reporting
  following once access is granted.
