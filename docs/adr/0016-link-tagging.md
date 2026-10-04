# ADR 0016: Tag links with UTM parameters at render time; leave clicks to web analytics

- Status: accepted; built
- Date: 2026-09-29

## Context

A product that posts through Araldo wants to know which posts bring people
to its site. That question belongs to its web analytics (Plausible, Google
Analytics, Matomo), which already count visits and read UTM parameters.
Storing clicks ourselves would mean running redirect links and keeping a
clickstream: the link shortening and analytics [ADR 0001](0001-scope.md)
puts out of scope, and an event store unlike the rest of our data.

UTM parameters make links longer. X and Mastodon count every URL as 23
characters, but Bluesky counts each one in full, so tagged links could push
a post over its 300-grapheme limit.

## Decision

1. **A brand lists the sites to tag** (`utm_domains`). Empty, the default,
   means no tagging. Only links to a listed domain or its subdomains are
   tagged; links to other sites are someone else's and stay as written.
2. **Each tagged link gets** `utm_source` (the channel's network),
   `utm_medium=social`, `utm_campaign` (the template's key, when there is
   one) and `utm_content` (the post's ID). A link that already has any
   `utm_` parameter is left alone: its author chose.
3. **Tagging happens at render time**, per channel, before the platform's
   rules measure the text, so limits and fit modes see the links as they
   will be posted. A preview uses the zero post ID, which has a real ID's
   length.
4. **Bluesky posts show short links**, the way the Bluesky app does: the
   text holds `host/path…` (`platform.ShortLink`, mirroring `toShortUrl` in
   `@atproto/api`) and a link facet holds the full URL. The Bluesky rule
   (`counting: bluesky`) measures that short text, so a tagged link costs
   no more than an untagged one.

## Alternatives considered

- **Redirect links with our own click counts.** Precise per post, but a
  second kind of data store, a public redirect endpoint to run, and a job
  web analytics already does.
- **Tag every link.** Would add our parameters to other people's sites.
- **Leave tagging to the caller.** Every product would repeat it, and it
  could not know the post's ID or the network in time.

## Consequences

- Web analytics can break visits down by network, template and post, with
  nothing extra to run.
- The rendered text depends on the post's ID, so a preview and the post
  differ only in that ID.
- Bluesky readers see short links; the full URL is one tap away, as in the
  Bluesky app.
