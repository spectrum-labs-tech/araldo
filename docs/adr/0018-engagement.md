# ADR 0018: Read each published post's engagement on a fixed schedule and keep every reading

- Status: proposed
- Date: 2026-10-02

## Context

Posting is half of promotion; the other half is learning which posts work.
Someone promoting a product wants to know which announcements, templates
and networks earn likes, reposts and replies, without opening every post on
every platform. [ADR 0001](0001-scope.md) puts "basic per-post metrics" in
scope and analytics beyond that out; [ADR 0016](0016-link-tagging.md)
leaves clicks to web analytics through UTM parameters.

Platforms report engagement differently. Bluesky's public AppView returns
like, repost, reply and quote counts for up to 25 posts per request, with
no sign-in (and sign-ins are tightly rate limited). Mastodon returns
favourite, boost and reply counts for one status at a time. Discord's
webhooks and Telegram's Bot API report nothing comparable. No platform
Araldo supports today reports views, though X, Threads, Instagram and
Facebook do.

## Decision

1. **Engagement is four counts and an optional fifth**: likes (Bluesky
   likes, Mastodon favourites), reposts (reposts, boosts), replies, quotes,
   and views where the platform reports them. Nothing else: no follower
   counts, no demographics, no clicks.
2. **Adapters read it** through an optional interface,
   `EngagementReader`, given the parts a target published. A part the
   platform no longer has means the post was deleted.
3. **A thread counts as one post**: the counts of its parts are added up,
   less the replies that are the thread's own parts.
4. **Readings follow a schedule**, by the time since publishing: 1 hour,
   6 hours, 1 day, 3 days, 7 days and 30 days, then stop. An `opsched`
   task (`engagement.collect`) reads whatever is due, a channel's posts in
   one batch. Every reading is kept, so a post's curve can be drawn; the
   latest is shown on the target. Targets published before this existed
   are read once soon after the upgrade and then follow the schedule.
5. **Failures never touch publishing**: a rate limit or an outage moves the
   next reading later; a revoked channel is retried less often; a deleted
   post stops being read and keeps its last counts. A platform without a
   reader is marked `unsupported`.
6. **The sandbox invents counts**, stable per post, so test mode has
   something to build reports against. The API says so in its docs.
7. **Reports aggregate the latest readings** of targets published in a
   window (`/v1/engagement/summary`), grouped by post, channel or template
   and sorted by engagement (the sum of the four counts). The dashboard's
   Performance page shows the same.

## Alternatives considered

- **Reading on demand** when a post is viewed. Slow, rate-limit hungry, and
  no history to compare a post's first day against another's.
- **Polling every published post every hour.** Most engagement arrives in
  the first day; reading a month-old post hourly wastes the platforms' rate
  limits for numbers that barely move.
- **A separate analytics store (ClickHouse).** One small row per reading is
  well within Postgres for any install Araldo targets.

## Consequences

- Each new adapter should implement `EngagementReader` where its platform
  allows; the ones that report views will fill that column.
- A published post costs six reads over a month.
- Numbers are as of the last reading, up to a few days old for older posts;
  the API and dashboard show when they were read.
