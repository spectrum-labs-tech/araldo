# ADR 0015: A server-rendered dashboard embedded in the binary

- Status: proposed
- Date: 2026-09-28

## Context

The API is the product, but people still need a dashboard to sign in, set up
MFA, connect channels (OAuth needs a browser), edit templates with a live
preview, approve posts, manage keys and webhooks, and see what happened.
Self-hosting should remain "one binary and a Postgres".

## Decision

1. **Server-rendered HTML** (`html/template`) with a little hand-written
   JavaScript only where a page needs it (template preview), styled by one
   hand-written stylesheet built on CSS custom properties. The same
   server-rendered approach as caseline's ADR 0005.
2. **Embedded in the binary** (`embed.FS`) and served by `araldo server`
   next to the API, on the same origin.
3. **The dashboard calls services directly**, never the HTTP API, and uses
   session cookies with CSRF tokens ([ADR 0007](0007-authentication-and-mfa.md)).
   The API uses keys only. This keeps each surface's authentication simple.
4. **No Node, at runtime or to build.** The stylesheet is plain CSS with
   design tokens. If it grows past what one file can hold, adopt
   Tailwind's standalone binary (no Node) with the compiled output
   committed, as caseline does.
5. **Accessibility:** WCAG 2.1 AA. Forms work without JavaScript.
6. **First screens:**
   - sign-in and MFA;
   - org, brand and member settings;
   - connecting channels;
   - API keys, and webhooks with their delivery log;
   - the template editor with a per-provider preview;
   - the queue and calendar, and approvals;
   - events and the request log.

## Alternatives considered

- **A SvelteKit single-page app** (open-b00ks). A second codebase, a Node
  build, and an API surface that would have to accept cookies as well as
  keys.
- **Dashboard as a pure API client.** Tidy in theory, but every screen
  would need public endpoints first, including sensitive ones such as MFA
  enrollment that do not belong in the public API.

## Consequences

- One image, one origin, one set of sessions.
- Highly interactive pieces (for example a drag-and-drop calendar) will need
  some hand-written JavaScript; if that grows large, revisit.
