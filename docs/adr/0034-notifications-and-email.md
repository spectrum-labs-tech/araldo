# ADR 0034: Notifications, in the dashboard and by email

- Status: accepted; built
- Date: 2026-10-05
- Builds on: [ADR 0004](0004-tenancy-roles-approvals.md), [ADR 0007](0007-authentication-and-mfa.md), [ADR 0012](0012-events-and-webhooks.md), [ADR 0033](0033-single-sign-on.md)

## Context

Araldo tells integrations what happened through events and webhooks
(ADR 0012), but tells people nothing. An approver does not learn a post is
waiting for them, an owner does not learn a channel needs reconnecting,
and nobody hears that their password changed or a CLI was signed in as
them. Araldo also sends no email, so a forgotten password needs an owner
or the operator (`araldo admin users reset-password`), and invitations are
links the inviter must pass on.

Email newsletters (ADR 0024) already go through each brand's own mail
account; that is the brand's audience and stays as it is. This is mail
from the install to its own users.

## Decision

1. **The install sends email over SMTP**, configured by the operator
   (`ARALDO_SMTP_*`: host, port, username, password, from address, TLS
   mode). Every provider accepts SMTP, so this adds no dependency and
   prefers none. Without it, Araldo works as before: email is suppressed,
   invitations are links to share, and resets go through the operator.
   Every message is text and HTML.
2. **Account links are sent at once**, in the request, outside
   notifications: password resets now, address verification later.
   - "Forgot your password?" on the sign-in page asks for an email and
     always answers the same, so it reveals no accounts. It is rate
     limited per network and per account.
   - The link holds a random token, stored hashed, single use, for 30
     minutes. Setting the new password signs the person out everywhere
     and revokes their CLI tokens, as changing it does.
   - An address at a domain an org signs in with by single sign-on (ADR
     0033) gets a link to sign in that way instead: its provider manages
     how that person signs in.
3. **A notification is one row per person**, written in the same
   transaction as the change that caused it, as events are. Its subject,
   text and link are written then, so they stay true. Each channel gets a
   delivery row: sent, queued, suppressed (with why), or failed. That
   records what went where.
4. **Types are declared in Go**, each with who receives it, its channels,
   its defaults and the event or action that raises it; a test keeps the
   list, the templates and the preferences page in step. The first:
   - *Org* (per org, by role): a post needs attention (its author and the
     org's admins), a target failed (its author), a channel needs
     reconnecting (admins), a post or newsletter waits for approval
     (members who may approve), a member joined (owners).
   - *Account* (the person, any org): password changed or reset, a
     passkey added, two-factor authentication turned off, a CLI signed in
     as them. These cannot be turned off, and always go by email when
     email is configured.
   The person who caused a change is not notified of it, and org
   notifications are raised in live mode only, so trying things in test
   mode tells no one.
5. **Channels are the dashboard and email.** The dashboard shows an
   unread count in the header and an inbox, marked read when opened or
   all at once, with no JavaScript needed. Browser push can come later as
   a third channel.
6. **Preferences are per person, per org, per type, per channel.** The
   dashboard is on by default; email is on by default only where a person
   must act (approvals, channels to reconnect, posts needing attention).
   Every notification email has a one-click unsubscribe (RFC 8058
   `List-Unsubscribe` and `List-Unsubscribe-Post`) and a footer link,
   both a signed link that turns off that type's email for that person in
   that org, without signing in.
7. **Email deliveries are sent by a periodic task** (`notifications.send`,
   every minute) on the task scheduler, claiming queued rows with
   `FOR UPDATE SKIP LOCKED` so workers never send one twice. A failure is
   retried with backoff, up to six attempts over about a day, then marked
   failed. A person receives at most 30 notification emails an hour; past
   that they are suppressed (the dashboard still has them).
8. **Read notifications are kept 90 days**, unread ones a year, then
   pruned.

## Consequences

- People learn what needs them without watching the dashboard, and
  security changes to their account reach them where an attacker in the
  dashboard cannot hide them.
- Self-service password reset makes a hosted install workable without
  support tickets for every forgotten password.
- Each new kind of thing worth telling someone is a type, a template and a
  test, not new plumbing.
- Email depends on the operator's SMTP service and its sender reputation;
  Araldo sends only what people asked for, with unsubscribe in every
  notification email, which bulk-sender rules require.
- A daily digest for busy types is left for later; the hourly cap keeps
  the volume sane meanwhile.
