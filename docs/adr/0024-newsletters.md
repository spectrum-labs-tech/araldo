# ADR 0024: Newsletters are designed and scheduled in Araldo and sent by the email provider, which owns the list

- Status: proposed
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
   like platforms ([ADR 0009](0009-platform-adapters.md)). Brevo is the
   first; Listmonk (self-hosted) and Buttondown follow when someone needs
   them. A sandbox provider in test mode sends nothing and invents numbers.

### Accounts and issues

3. **A mail account belongs to a brand** (`mail_accounts`): the provider,
   its API key encrypted ([ADR 0008](0008-encryption.md)), the sender
   (name and an address the provider has verified), a reply-to address, and
   the default list. Connecting one needs `channels:write`.
4. **An issue is its own object** (`newsletter_issues`), not a post: posts
   are short text split per platform; an issue is a document. It has a
   subject, preview text, a body, the lists it goes to, a send time, and
   its status: `draft`, `pending_approval`, `scheduled`, `handed_off`,
   `sent`, `canceled` or `failed`. Its body is Markdown written by hand, or
   rendered from a template with data ([ADR 0010](0010-templates.md)), so a
   product can send "this week's featured builds" from its own data.
   Scopes: `newsletters:read`, `newsletters:write`.

### Design

5. **Araldo renders email-safe HTML itself**, from the Markdown and a
   small set of blocks (heading, text, image from `/v1/media`, button,
   divider, and later a card for a published post), into tested
   table-based HTML with inline styles, 600 pixels wide, readable in dark
   mode, with a plain-text version beside it. What is previewed is what is
   sent, as with posts.
6. **Each brand has an email theme**: logo, accent color, and a footer
   with the brand's postal address (anti-spam laws require one). The
   adapter adds the provider's own unsubscribe link to the footer (Brevo's
   `{{ unsubscribe }}`), so every issue can be left, and an issue cannot be
   scheduled without the address.
7. **Every link is tagged**: `utm_medium=email`, `utm_source` the brand's
   newsletter, `utm_campaign` the issue, and `utm_content` the link's
   place in it, so signups from an issue show in the brand's own analytics
   next to social and paid traffic. The provider's own click tracking may
   wrap the links; the tags survive it.
8. **Test sends go through the provider** to addresses given at that
   moment and never stored, a handful at a time.

### Approval and scheduling

9. **Issues follow the brand's approval policy** like posts
   ([ADR 0004](0004-tenancy-roles-approvals.md)); approving needs the
   explicit `posts:approve`. They can be moved like posts
   ([ADR 0022](0022-slots-at-approval.md)) until they are sent.
10. **Sending is handed off before it is due.** When an issue is
    scheduled, Araldo creates it at the provider as a draft and keeps that
    draft in step with every edit or move. Within 24 hours of the send
    time it schedules the campaign at the provider, so the send no longer
    depends on Araldo running at that moment. After that, moving or
    canceling the issue changes or cancels the provider's campaign;
    nothing changes once it is sent.
11. **At most once, as with posts** ([ADR 0011](0011-publishing.md)). The
    provider's campaign ID is stored as soon as it exists, and the campaign
    is tagged with the issue's ID. A create whose result is unknown is
    reconciled by finding the campaign with that tag before anything is
    created again; a hand-off whose result is unknown goes to
    `needs_attention` rather than risk a second send.

### Measurement

12. **Results are read from the provider** on a schedule (1 hour, 1, 3, 7
    and 30 days after sending): delivered, unique opens, unique clicks,
    unsubscribes, bounces and complaints. They show next to social
    engagement and ad spend. Opens are inflated by mail clients that load
    images on their own, so the dashboard leads with clicks.

### Deliverability

13. **Newsletters go out from a sending subdomain of their own** per brand
    (`news.<brand domain>`), with its own DKIM, SPF and DMARC records,
    verified at the provider and kept apart from the domain transactional
    mail uses. The dashboard's guide shows how; the DNS lives with each
    install's infrastructure, not in Araldo.

### Surfaces

14. **API**: `/v1/mail_accounts` (connect, list, lists of the provider) and
    `/v1/newsletters` (create, preview, update while a draft, approve,
    reject, reschedule, cancel, test send, results). Contract first
    ([ADR 0005](0005-api-conventions.md)).
15. **Dashboard**: a Newsletters page with the editor, a live preview
    (desktop, mobile, plain text) and a guide; the brand page gets the
    email theme.
16. **MCP**: tools to draft and preview an issue, so an agent can write
    "this week's best posts" from engagement. Approval stays with people.

### Phases

- **Phase 1:** mail accounts, issues, the renderer and theme, approval and
  scheduling, the Brevo adapter with hand-off, test sends, results.
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
- **Storing subscribers to sync between providers.** Privacy and deletion
  duties for a convenience; moving lists is a provider export.

## Consequences

- Araldo's scope grows from social posts to newsletters, still without any
  audience data of its own.
- New tables (`mail_accounts`, `newsletter_issues`, results), a package per
  provider, scopes, events (`newsletter.*`) and tasks (hand-off, results).
- An issue's HTML is Araldo's to keep working across mail clients; the
  renderer gets tests against the clients' known quirks.
- Each brand that sends newsletters needs a sending subdomain set up once,
  outside Araldo.
