# ADR 0001: Araldo is a distribution API for developers; callers bring the content

- Status: accepted; ads narrowed by [ADR 0023](0023-paid-promotion.md)
- Date: 2026-09-28

## Context

Getting users is the hardest part of launching software. Every product needs
to announce what it ships (a new feature, a featured build, a new brand) on
every network its users read, on a schedule, without someone copying text
into five web forms.

What exists:

- **Hosted schedulers** (Buffer, Hootsuite, HubSpot) are built for marketers
  clicking through a calendar. Their APIs are secondary or absent.
- **Postiz** is open source (AGPL) with many platforms, but it is a Node app
  that needs Temporal and Elasticsearch to self-host, and its public API is a
  subset of the internal one (no rescheduling with an API key, for example).
- **Publishing built into each product** works, with an adapter per
  platform and a job table with retries, but every product rebuilds it: one
  tenant, credentials in environment variables, post text hard-coded.

## Decision

1. **Araldo is an API product.** Every capability ships in the API first or
   together with the dashboard, and the dashboard calls the same services.
   The developer integrating a product is the primary user; the person
   approving and scheduling posts in the dashboard is the second.
2. **Social first, email later.** Newsletters (lists, subscribers, sending
   through SES, Brevo, Resend or SMTP) reuse the same tenancy, templates and
   API once social publishing is solid. See [roadmap](../roadmap.md).
3. **Callers bring the content; Araldo does not run AI.** The product that
   knows what is worth announcing (and has the tools to look it up) writes
   the words. It sends either a template reference with data, or a finished
   body per platform. Araldo owns the deterministic part:
   - each platform's rules (length and how it is counted, media required,
     threads, links), enforced before anything is scheduled;
   - a **preview** endpoint that renders content and returns every
     violation with a stable code, so a caller's AI agent can correct itself
     and try again;
   - an MCP server exposing preview and schedule as tools.

   We revisit this if several callers end up repeating the same "adapt this
   announcement for each network" prompt; that would be the signal to offer
   adaptation as an optional, bring-your-own-key feature.
4. **Out of scope:** reading or answering replies (a social inbox), ads
   (except small, bounded promotions, [ADR 0023](0023-paid-promotion.md)),
   social listening, link shortening, and analytics beyond basic per-post
   metrics (later).

## Alternatives considered

- **Adopt Postiz.** Fastest to a working scheduler, but its public API is
  second-class, the self-hosted stack is heavy, and its direction (AI agents
  inside the product) is not ours.
- **Keep publishing inside each product.** Every product would build and
  maintain the same adapters again.
- **Generate content inside Araldo.** It would need each product's domain
  knowledge and tools, which the product already has.

## Consequences

- The API is the product: its conventions ([ADR 0005](0005-api-conventions.md))
  get the care most projects give their UI.
- Products stop publishing directly; they call Araldo.
- Without built-in AI, Araldo is useful only to callers that can produce
  content. The template engine ([ADR 0010](0010-templates.md)) covers
  callers without AI.
