# ADR 0032: A request log, for the org's developers

- Status: accepted; built
- Date: 2026-10-05
- Builds on: [ADR 0005](0005-api-conventions.md) (request IDs), [ADR 0028](0028-cli-as-api-client.md)

## Context

A developer integrating with Araldo sees only their own side: what their
code sent, and what came back if they logged it. When a post never
appears, or a key is refused, the first question is "did Araldo get my
request, and what did it say?", and today only the operator's server logs
can answer it.

## Decision

1. **Every authenticated request in an org is logged**: method, route
   pattern and path, status, the problem's code if it failed, duration,
   the API key or CLI sign-in that made it, the request ID, and when. Not
   bodies, not query strings, not headers: the log says what happened, not
   what was in it, and holds nothing secret.
2. **It is written in the background**, in batches, so a request never
   waits on it. When the database falls behind and the buffer fills,
   requests go unlogged (and the server says so in its own logs) rather
   than slowing the API.
3. **It is kept 14 days**, then pruned.
4. **Admins and owners read it** in the dashboard, under Developers →
   Request log, in the mode they are in, filtered by outcome. The request
   ID ties an entry to the caller's own logs and to the operator's.
5. The operator API's requests, which belong to no org, are not in it.

## Consequences

- A refused or failed request can be traced from the dashboard without
  asking the operator.
- Each API request costs a row; at Araldo's volumes, with 14 days kept,
  that is small, and the batch insert keeps it off the request's path.
