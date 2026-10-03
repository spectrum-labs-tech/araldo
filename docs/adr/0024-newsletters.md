# ADR 0024: Newsletters are designed and scheduled in Araldo and sent by the email provider, which owns the list

- Status: accepted; phase 1 built
- Date: 2026-10-03

## Context

Newsletters are the other half of a small product's marketing: the
people who asked to hear from you, reached without an algorithm in the way.
Araldo already has what designing and scheduling one needs: brands,
templates with data and previews, approval, scheduling, a media library,
UTM tagging ([ADR 0016](0016-link-tagging.md)), engagement reading
([ADR 0018](0018-engagement.md)), and an MCP server an agent can draft with
([ADR 0020](0020-mcp.md)).

What it should not take on is the list. Holding subscribers means consent
records, unsubscribes honored everywhere at once, bounces and complaints,
data-subject requests, and the sending reputation that decides whether any
of it arrives. Email providers (Brevo, Listmonk, Buttondown, Mailchimp) do
all of that, and do it better.

Marketing mail also draws more complaints than transactional mail. Sent
from the same domain, those complaints push password resets and receipts
into spam.

## Decision

### The split

1. **Araldo designs, approves and schedules; the provider sends.** The
   provider owns the subscribers, the unsubscribe page, bounces,
   complaints and consent. Araldo stores no subscriber address: only a
   list's provider ID, name and size, and the counts the provider reports.
2. **Providers are adapters**, one package each under `internal/email/`,
   like platforms ([ADR 0009](0009-platform-adapters.md)), and a brand can
   use any number of them at once. Brevo is the first; Listmonk
   (self-hosted) and Buttondown follow when someone needs them. A sandbox
   provider in test mode sends nothing and invents numbers. A provider that
   relays through another service (Listmonk through SES or Brevo's SMTP) is
   configured in that provider; Araldo talks to whichever one owns the
   list.

### Accounts and issues

3. **A brand connects any number of mail accounts** (`mail_accounts`),
   across providers and several of one provider: the provider, its API key
   encrypted ([ADR 0008](0008-encryption.md)), the sender (name and an
   address that provider has verified), a reply-to address, and default
   audiences. Connecting one needs `channels:write`.
4. **Audiences are whatever the provider groups subscribers by**: Brevo's
   lists and segments, Listmonk's lists, Buttondown's tags. An audience is
   a mail account's provider ID, a name, its kind and its size, read from
   the provider and never its members.
5. **An issue is its own object** (`newsletter_issues`), not a post: posts
   are short text split per platform; an issue is a document. It has a
   subject, preview text, a body, a send time, and the audiences it goes
   to, which may span mail accounts and providers. Its body is Markdown
   written by hand, or rendered from a template with data
   ([ADR 0010](0010-templates.md)), so a product can send "this week's
   featured builds" from its own data. Scopes: `newsletters:read`,
   `newsletters:write`.
6. **An issue has one delivery per mail account it goes to**
   (`newsletter_deliveries`), as a post has one target per channel
   ([ADR 0011](0011-publishing.md)): that account's audiences, its own
   campaign at the provider, hand-off, status and results. The issue's
   status (`draft`, `pending_approval`, `scheduled`, `sending`, `sent`,
   `partially_sent`, `canceled`, `failed`) is derived from its deliveries.
   One provider failing never holds back the others.
7. **Araldo cannot remove duplicates across providers**: without addresses
   it cannot know that someone on a Brevo list is also on a Listmonk list,
   and would send them two copies. Within one account, the provider sends
   each person one copy however many of its audiences they are in. Keep
   lists on different providers apart (a newsletter per provider, or one
   provider per audience).

### Design

8. **Araldo renders email-safe HTML itself**, from the Markdown and a
   small set of blocks (heading, text, image from `/v1/media`, button,
   divider, and later a card for a published post), into tested
   table-based HTML with inline styles, 600 pixels wide, readable in dark
   mode, with a plain-text version beside it. What is previewed is what is
   sent, as with posts.
9. **Each brand has an email theme**: logo, accent color, and a footer
   with the brand's postal address (anti-spam laws require one). Each
   delivery's adapter adds its provider's own unsubscribe link to the
   footer (Brevo's `{{ unsubscribe }}`, Listmonk's `{{ UnsubscribeURL }}`),
   so every copy can be left wherever it came from; an issue cannot be
   scheduled without the address.
10. **Every link is tagged**: `utm_medium=email`, `utm_source` the
   provider, `utm_campaign` the issue, and `utm_content` the link's place
   in it, so signups from an issue show in the brand's own analytics
   next to social and paid traffic. The provider's own click tracking may
   wrap the links; the tags survive it.
11. **Test sends go through a chosen mail account's provider** to addresses given at that
   moment and never stored, a handful at a time.

### Approval and scheduling

12. **Issues follow the brand's approval policy** like posts
   ([ADR 0004](0004-tenancy-roles-approvals.md)); approving needs the
   explicit `posts:approve`. They can be moved like posts
   ([ADR 0022](0022-slots-at-approval.md)) until they are sent.
13. **Sending is handed off before it is due**, per delivery. When an
    issue is scheduled, Araldo creates each delivery's campaign at its
    provider as a draft and keeps it in step with every edit or move.
    Within 24 hours of the send time it schedules each campaign at its
    provider, so the send no longer depends on Araldo running at that
    moment. After that, moving or canceling the issue changes or cancels
    every provider's campaign; nothing changes once a delivery is sent.
14. **At most once, as with posts** ([ADR 0011](0011-publishing.md)). A
    delivery's campaign ID is stored as soon as it exists, and the campaign
    is tagged with the delivery's ID. A create whose result is unknown is
    reconciled by finding the campaign with that tag before anything is
    created again; a hand-off whose result is unknown goes to
    `needs_attention` rather than risk a second send.

### Measurement

15. **Results are read from each provider** on a schedule (1 hour, 1, 3, 7
    and 30 days after sending): delivered, unique opens, unique clicks,
    unsubscribes, bounces and complaints, per delivery and added up per
    issue. They show next to social engagement and ad spend. Opens are inflated by mail clients that load
    images on their own, so the dashboard leads with clicks.

### Deliverability

16. **Newsletters go out from a sending subdomain of their own** per brand
    (`news.<brand domain>`), with its own DKIM, SPF and DMARC records,
    verified at each provider that sends for it and kept apart from the
    domain transactional mail uses. The dashboard's guide shows how; the DNS lives with each
    install's infrastructure, not in Araldo.

### Surfaces

17. **API**: `/v1/mail_accounts` (connect, list, and each account's
    audiences) and `/v1/newsletters` (create, preview, update while a
    draft, approve, reject, reschedule, cancel, test send, results by
    delivery). Contract first
    ([ADR 0005](0005-api-conventions.md)).
18. **Dashboard**: a Newsletters page with the editor, a live preview
    (desktop, mobile, plain text) and a guide; the brand page gets the
    email theme.
19. **MCP**: tools to draft and preview an issue, so an agent can write
    "this week's best posts" from engagement. Approval stays with people.

### Phases

- **Phase 1:** mail accounts and audiences, issues with a delivery per
  account, the renderer and theme, approval and scheduling, the Brevo
  adapter with hand-off, test sends, results. Built for several providers
  from the start, with Brevo and the sandbox as the first two.
  Built as decided, with one change to decision 13: a delivery's
  campaign is created at its provider at hand-off, a day before the send,
  rather than as a draft when the issue is scheduled. Until then an issue
  lives only in Araldo, so edits, moves and unscheduling touch no
  provider, and a hand-off is one request (create with its send time)
  that a retry can find by its tag. Test sends go through the provider's
  transactional API instead of a draft campaign, so they reach any
  address. An unscheduled issue returns to draft and needs approval
  again; a rejected one returns to draft with the note. Phase 2's MCP
  tools came early: an agent can preview an issue, save it as a draft and
  follow results, and a person schedules it.
- **Phase 2:** templates with data for issues, a "top posts" block from
  engagement, MCP tools.
- **Phase 3:** more providers (Listmonk, Buttondown), and send slots per
  brand.

## Alternatives considered

- **Sending mail from Araldo** (SMTP or a sending API). Araldo would then
  own the list: consent, unsubscribes, bounces, complaints and reputation.
  That is a provider's whole job.
- **Designing in the provider's own editor.** No brand templates, no
  approval, no UTM consistency with the brand's posts and ads, and nothing
  an agent can draft against through Araldo.
- **Scheduling only in Araldo** and telling the provider to send at the
  time. Every send would depend on Araldo being up at that minute; the
  hand-off moves that risk to a provider built to carry it.
- **Storing subscribers to sync between providers**, or to remove
  duplicates across them. Privacy and deletion duties for a convenience;
  moving lists is a provider export, and keeping each list on one provider
  avoids duplicates.
- **One mail account per issue.** Simpler, but a brand moving from one
  provider to another, or keeping a self-hosted list beside a hosted one,
  would have to send the same issue twice by hand.

## Consequences

- Araldo's scope grows from social posts to newsletters, still without any
  audience data of its own.
- **Switching providers is cheap.** Issues, templates, the theme, approval
  history, past results and the tags tying signups to issues stay in
  Araldo; only the subscriber list moves, by the providers' own export and
  import. Connect the new account, point the next issue at it, and keep
  sending; a gradual move can send to both while each subscriber is on one
  side only. The same holds for platforms and ad networks: Araldo keeps the
  work, adapters are replaceable.
- New tables (`mail_accounts`, `newsletter_issues`,
  `newsletter_deliveries`, results), a package per provider, scopes,
  events (`newsletter.*`) and tasks (hand-off, results).
- An issue's HTML is Araldo's to keep working across mail clients; the
  renderer gets tests against the clients' known quirks.
- Each brand that sends newsletters needs a sending subdomain set up once,
  outside Araldo.
