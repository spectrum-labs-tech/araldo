# Operating Araldo

## Configuration

Everything comes from environment variables.

| Variable | Required | Meaning |
|---|---|---|
| `ARALDO_DATABASE_URL` | yes | Postgres URL (`DATABASE_URL` also works). The role must own the schema: Araldo runs its own migrations. |
| `ARALDO_MASTER_KEYS` | this or Transit | Local master keys, `id:base64key[,id:base64key…]`, primary first ([ADR 0008](adr/0008-encryption.md)). Or `ARALDO_MASTER_KEYS_FILE`. |
| `ARALDO_TRANSIT_ADDR`, `ARALDO_TRANSIT_KEY` | this or local keys | A master key in an OpenBao or Vault Transit engine (see Keys). With local keys too, Transit is primary. |
| `ARALDO_TRANSIT_TOKEN` | with Transit | A token that can encrypt and decrypt with the key. Or `ARALDO_TRANSIT_TOKEN_FILE`, or log in with a role instead. |
| `ARALDO_TRANSIT_ROLE` | with Transit | Log in through the Kubernetes or JWT auth method as this role, with the pod's ServiceAccount token (`ARALDO_TRANSIT_JWT_FILE`; default the standard path). `ARALDO_TRANSIT_AUTH_PATH` is the method's mount, default `kubernetes`. |
| `ARALDO_TRANSIT_MOUNT` | | The Transit engine's mount, default `transit`. |
| `ARALDO_BASE_URL` | yes | The public URL, e.g. `https://araldo.example.com`. |
| `ARALDO_LISTEN` | | HTTP address, default `:8080`. |
| `ARALDO_AUTO_MIGRATE` | | Migrate at startup, default `true`. The Helm chart sets it to `false` and migrates in a hook instead (see Kubernetes). |
| `ARALDO_CLIENT_IP_HEADER` | | Trusted proxy header with the client IP (e.g. `CF-Connecting-IP`), for sign-in rate limits. |
| `ARALDO_ALLOW_PRIVATE_NETWORKS` | | Non-public addresses platform adapters may reach, such as a Mastodon on your LAN: `true` (every one but link-local, where cloud metadata answers) or a list like `192.168.1.20,10.8.0.0/16`. Off by default. |
| `ARALDO_ALLOW_PRIVATE_WEBHOOKS` | | The same for webhook deliveries and media fetched by URL. Every org chooses those URLs, so with other tenants, list only the hosts they need. Off by default. |
| `ARALDO_INSECURE_COOKIES` | | Plain-HTTP development only. |
| `ARALDO_LOG_LEVEL` | | `debug`, `info` (default), `warn`, `error`. |
| `ARALDO_S3_BUCKET` | | Store new media in this S3-compatible bucket instead of Postgres (see Media). |
| `ARALDO_S3_ENDPOINT` | with a bucket | The service URL, e.g. `https://<account>.r2.cloudflarestorage.com`. |
| `ARALDO_S3_ACCESS_KEY_ID`, `ARALDO_S3_SECRET_ACCESS_KEY` | with a bucket | Credentials that can put, get and delete objects. |
| `ARALDO_S3_REGION` | | Default `auto` (R2); AWS needs the bucket's region. |
| `ARALDO_S3_PREFIX` | | Key prefix, default `media/`. |
| `ARALDO_MAX_VIDEO_BYTES` | | The largest video accepted, in bytes; default 1073741824 (1 GiB). Video needs S3. |
| `ARALDO_SIGNUP_URL` | | Where people create an account and org, on an install that hosts orgs for others (see Hosting orgs for others). The sign-in page links there, and members can no longer create orgs themselves. |
| `ARALDO_BILLING_URL` | | Where owners manage billing; they get a Billing link that sends them there with a signed hand-off. |
| `ARALDO_BILLING_LINK_KEY` | with a billing URL | The key that signs the hand-off, at least 32 characters, shared with the billing service. Or `ARALDO_BILLING_LINK_KEY_FILE`. |

## First run

```sh
araldo admin keys generate --id k1          # keep this safe, outside the database
export ARALDO_MASTER_KEYS=k1:…
araldo admin bootstrap --email you@example.com --org "Your org" --brand "Your product"
araldo server & araldo worker         # or: araldo all
```

`bootstrap` prints a generated password once. Sign in, then turn on
two-factor authentication under **Your account**.

## Keys

- **Back up the master keys separately from the database.** Without them,
  connected channels must be reconnected (posts and history survive).
- Rotate: generate a new key, put it first in `ARALDO_MASTER_KEYS` (keep the
  old one after it), restart, run `araldo admin keys rotate`, then remove the old
  key once `araldo admin keys status` says it wraps nothing.
- `araldo admin keys status` lists each master key and how many data keys it
  wraps, and fails if any are wrapped by a key that is not configured.
- Signed media links survive both: they are signed with a key stored like a
  data key, not with the master key itself.

### Master keys in Transit

A local master key sits in the environment, so anyone who can read the
pods' environment or Secrets can read it. A key in an OpenBao or Vault
Transit engine never leaves it: Araldo asks Transit to wrap and unwrap data
keys, and caches the unwrapped data keys in memory. While Transit is
unreachable Araldo keeps running: data keys already cached keep working,
and what needs one that is not fails with `keys_unavailable` until Transit
is back (see Health).

Transit needs a key (`aes256-gcm96`, the default type) and a token or role
whose policy allows `update` on `<mount>/encrypt/<key>` and
`<mount>/decrypt/<key>`, nothing else. In Kubernetes, bind the role to the
pods' own ServiceAccount (the chart's `serviceAccount.create`) and to an
audience (`transit.audience`), so no other workload's token works.

To move an install from a local key to Transit:

1. Configure Transit and keep `ARALDO_MASTER_KEYS`: Transit becomes primary,
   and the local key still unwraps what it wrapped.
2. Run `araldo admin keys rotate` to rewrap every data key under Transit.
3. Check `araldo admin keys status`: the local key should wrap nothing.
4. Remove `ARALDO_MASTER_KEYS`, and keep the old key in offline escrow for
   database backups taken before step 2.

## Users

- `araldo admin users create --email …` and `araldo admin users reset-password --email …`
  print a generated password (or read one with `--password-stdin`).
- **Passkeys**: anyone can add one under **Your account → Passkeys** and
  then sign in with it alone; it counts as two-factor authentication, for
  orgs that require it too ([ADR 0007](adr/0007-authentication-and-mfa.md)).
  Passkeys belong to the host of `ARALDO_BASE_URL`: moving the dashboard to
  another host means adding them again.
- **Single sign-on**: an org's owners connect its OpenID Connect provider
  and verify its email domains by DNS under **Org → Single sign-on**
  ([ADR 0033](adr/0033-single-sign-on.md)). The redirect URI to register
  is `<ARALDO_BASE_URL>/login/sso/callback`. Providers are reached like
  webhooks, so one on a private network needs `ARALDO_ALLOW_PRIVATE_WEBHOOKS`.
  If an org requires single sign-on and its provider is gone,
  `araldo admin org update --org … --require-sso false` lets its members
  in with a password again.

## Request log

Under **Developers → Request log**, admins and owners see every API
request made with the org's keys and CLI sign-ins, in the mode they are
in, for 14 days ([ADR 0032](adr/0032-request-log.md)): method and path,
status and problem code, duration, which key, and the request ID that
ties it to the caller's own logs and to the server's. Bodies are not
kept. The `requests.prune` task deletes what is older.

## API keys for other services

`araldo admin apikeys create` makes a key without the dashboard, for provisioning
a service's key straight into its secret store. It prints only the key on
stdout, so pipe it rather than copying it:

```bash
araldo admin apikeys create --org "Your org" --brand your-product \
  --name "your-service staging" --scopes posts:write,templates:read,templates:write \
  | your-secret-store put …
```

The operator makes it, in the org named by `--org`, and the audit log says
so. Add `--live` for a live key and `--expires 8760h` to expire it.

### Administration by key

A key with no scopes holds every integration scope: `posts:read`,
`posts:write`, `templates:read`, `templates:write`, `channels:read`,
`channels:write`, `brands:read`, `brands:write`, `events:read`,
`webhooks:read`, `webhooks:write`, `ads:read`, `newsletters:read` and
`newsletters:write`. Four administrative scopes are held only when listed
by name, and only a member (here, or the dashboard) can grant them
([ADR 0019](adr/0019-administration-api.md)):

| Scope | Lets the key |
|---|---|
| `keys:write` | List, create, roll and revoke keys in its mode, with no more access than its own (`/v1/api_keys`). For a Terraform provider or a secrets rotator. |
| `posts:approve` | Approve or reject posts waiting for review (`/v1/posts/{id}/approve`, `/reject`), never one it created. For approving from Slack or your own tools. |
| `audit:read` | Read the audit log (`/v1/audit_events`): every change, and every refusal (`access.denied`, outcome `denied`, with the route and the reason; one per credential and route a minute). For exporting to a SIEM. |
| `ads:write` | Connect and remove ad accounts, and register ad networks' developer apps. |

Making fewer of a brand's posts need approval also needs `posts:approve`.
A key limited to one brand cannot read events or manage webhook
endpoints, which span every brand.

```bash
araldo admin apikeys create --org "Your org" --name "terraform" \
  --scopes brands:read,brands:write,channels:read,channels:write,templates:read,templates:write,webhooks:read,webhooks:write,keys:write
```

Listing any scope ends "full access", so a key that needs integration work
and administration lists both.

### Rotating keys

Roll a key in the dashboard, or have it roll itself with
`POST /v1/api_keys/self/roll`: the new secret has the same scopes, and the
old one keeps working for 24 hours (`overlap_hours`, up to 168) so the new
one can be deployed without downtime.

## Connecting platforms

Most platforms connect with credentials pasted into **Channels → Connect a
channel** in live mode; the form says where each comes from. Some connect
with a sign-in through a **developer app** you register with the platform
([ADR 0021](adr/0021-oauth-connections.md)): add it under **Channels →
Developer apps**, which shows the redirect URI to give the platform, then
connect channels through it. Tokens are stored encrypted and renewed before
they expire (the `channels.refresh` task); a channel whose token can no
longer be renewed shows *needs reauth* and reconnects the same way. Once a
day (once a week on X, which bills the app for each account lookup) the
`channels.check` task verifies every live channel's credentials with its
platform: revoked ones mark the channel *needs reauth* (with the
`channel.needs_reauth` event) before a post fails, and other failures (a
platform outage, a retired API version) show under the channel as the last
check's result.

**Apps for every org.** An install that hosts orgs other than its
operator's can register each platform's app once, for every org to sign in
through ([ADR 0030](adr/0030-install-wide-apps.md)). The client secret is
read from stdin and sealed with the install's key:

```sh
printf '%s\n' "$CLIENT_SECRET" | araldo admin apps add --provider linkedin --name "LinkedIn" --client-id CLIENT_ID
araldo admin apps list
araldo admin apps rename --app app_… --name "LinkedIn posting"
araldo admin apps remove --app app_…
```

`add` prints the redirect URI to register with the platform. Members see
these apps as *provided by this server*: they connect through them, but
cannot see their client IDs or change them, and an org's own app for the
platform is offered first. Every org shares the app's quota and its
standing with the platform. Removing one leaves what was connected through
it working until its token needs renewing, as removing an org's app does.

| Platform | How it connects |
|---|---|
| Bluesky | Handle and an app password. |
| Mastodon | Server and an access token (write:statuses, write:media, read:accounts). |
| Gab | An access token (write:statuses, write:media, read:accounts). Gab is one service, so there is no server to give. |
| X | A sign-in through your X app, below; or the app's API key and secret and the account's access token and secret, pasted (read and write). |
| LinkedIn | A sign-in through your LinkedIn app, below; or a member access token from its token tools (openid, profile, w_member_social), pasted. Tokens last 60 days. Posts go to the member's feed. |
| LinkedIn Pages | A sign-in through a second LinkedIn app with the Community Management API, below, choosing Pages: each is a channel, posting as the Page; or a token and the Page's ID, pasted. |
| Threads | A sign-in through your Threads app, below. |
| Facebook Pages, Instagram | A sign-in through your Meta app, below; you choose which Pages, or which Instagram accounts linked to them. |
| Pinterest | A sign-in through your Pinterest app, below, choosing boards: each board is a channel; or an access token and a board ID, pasted. |
| YouTube | A sign-in through your Google app, below; each YouTube channel the account manages is a channel. Posts are videos. |
| TikTok | A sign-in through your TikTok app, below. Posts are videos. |
| Discord, Telegram | A webhook URL; a bot token and chat. |

**X.** At developer.x.com, in your app's *User authentication settings*,
choose *Read and write* (Araldo needs no direct messages), leave *Request
email from users* off, choose *Web App, Automated App or Bot* (a
confidential client: Araldo keeps the secret on its server), add Araldo's
redirect URI (`{ARALDO_BASE_URL}/connect/x/callback`) as a callback URI,
and give your product's site as the website URL. Add the app's OAuth 2.0
client ID and secret (not the API key) under Developer apps, then
connect. The Free tier posts text only; images and video need Basic. If
you paste OAuth 1.0a keys instead, generate the access token after
choosing *Read and write*: a token keeps the permissions it was made
with. The sign-in asks for `tweet.read`, `tweet.write`,
`users.read`, `media.write` and `offline.access`; tokens last two hours and
are renewed automatically, each renewal replacing the refresh token. What
an app may post and read depends on its X API access tier.

**LinkedIn.** At linkedin.com/developers, add the products *Sign In with
LinkedIn using OpenID Connect* and *Share on LinkedIn* to your app, and
add Araldo's redirect URI (`{ARALDO_BASE_URL}/connect/linkedin/callback`)
under Auth → Authorized redirect URLs. Add the app's client ID and secret
under Developer apps, then connect. Araldo needs both products: OpenID
Connect says who signed in, Share on LinkedIn lets it post. It posts to the
personal feed of the member who signs in, never to a company Page: posting
as a Page needs LinkedIn's Community Management API, which LinkedIn reviews
and grants only to an app with no other products (so a second app, verified
by a Page admin), and Araldo does not post to Pages yet. Asked for a use
case, your own Pages and ad accounts are *Direct Advertiser*. Araldo does
not use the Advertising API yet. Tokens last 60 days. LinkedIn gives
refresh tokens only to apps it has approved for them; without one, the
channel says a week ahead when to sign in again, and needs it once the
token expires.

**LinkedIn Pages.** To post as a company Page rather than a member, use a
second LinkedIn app with no other products, verified by a Page admin, and
request LinkedIn's *Community Management API* for it (the Development tier
covers your own Pages; the use case is *Direct Advertiser*). Once LinkedIn
grants it, add Araldo's redirect URI
(`{ARALDO_BASE_URL}/connect/linkedin_pages/callback`) under Auth →
Authorized redirect URLs, add the app under Developer apps as LinkedIn
Page, then connect: each Page you administer is offered as a channel, and
its posts appear as the Page. The sign-in asks for `r_organization_admin`
(to list your Pages) and `w_organization_social` (to post). Tokens last 60
days, as for members.

**YouTube.** In a Google Cloud project, enable the *YouTube Data API v3*,
configure the OAuth consent screen, and create an OAuth client of type *Web
application* with Araldo's redirect URI
(`{ARALDO_BASE_URL}/connect/youtube/callback`). Add its client ID and secret
under Developer apps, then connect. The sign-in asks for `youtube.upload` and
`youtube.readonly`; tokens last an hour and are renewed automatically. While
the consent screen is in *testing*, Google expires the refresh token after
seven days, so publish the app. A post is one video: the text's first line
is its title (100 characters at most) and the rest its description; videos
are public. Each upload costs 1,600 of the project's 10,000 daily quota
units, so about six a day until Google raises it; past that, uploads wait
until the quota resets at midnight Pacific time. Videos over 15 minutes need
a verified YouTube account.

**TikTok.** At developers.tiktok.com, create an app with the *Login Kit*
and *Content Posting API* products and the scopes `user.info.basic` and
`video.publish`, and add Araldo's redirect URI
(`{ARALDO_BASE_URL}/connect/tiktok/callback`). Add its client key and secret
under Developer apps, then connect. Tokens last a day and are renewed
automatically. Before each post Araldo asks TikTok what the account may
post: its longest video, and the privacy levels it may use (public when
allowed). Until TikTok audits the app, it can post only to accounts set to
private, and its posts are private too; the error says so.

**Pinterest.** At developers.pinterest.com/apps, create an app, and add
Araldo's redirect URI (`{ARALDO_BASE_URL}/connect/pinterest/callback`).
Add the app's ID and secret under Developer apps, then connect and choose
the boards to pin to; each board becomes a channel. The sign-in asks for
`boards:read`, `pins:read`, `pins:write` and `user_accounts:read`; tokens
last 30 days and are renewed automatically. A pin needs one image. The
first link in the text becomes the pin's destination, and a first line of
up to 100 characters followed by more text becomes its title. Pinterest
reviews an app before granting it Standard access, and limits what it can
do until then: check the access level on the app's page.

**Threads.** At developers.facebook.com, create an app with the *Access the
Threads API* use case; add the permissions `threads_basic`,
`threads_content_publish` and `threads_manage_insights`; under its
settings add Araldo's redirect URI (`{ARALDO_BASE_URL}/connect/threads/callback`)
to the redirect callback URLs; and, while the app is in development, add
the Threads accounts you will connect as testers (they accept in Threads
→ Settings → Website permissions). Add the app's Threads app ID and secret
under Developer apps, then connect. Threads fetches images from a link to
the install, so posts with images need `/v1` reachable from the internet;
the engagement it reports includes views.

**Facebook Pages and Instagram.** At developers.facebook.com, create a
Business app with Facebook Login for Business; request `pages_show_list`,
`pages_manage_posts`, `pages_read_engagement` (Pages) and
`instagram_basic`, `instagram_content_publish`, `business_management`
(Instagram, which must be a professional account linked to a Page); add
Araldo's redirect URIs (`{ARALDO_BASE_URL}/connect/facebook/callback` and
`…/connect/instagram/callback`) to the valid OAuth redirect URIs. While the
app is in development it works for people with a role on it; publishing
for others needs Meta's app review. The same app can serve both: add it
under Developer apps once as Facebook and once as Instagram. Page tokens
do not expire. Facebook images are uploaded; Instagram fetches them from a
link to the install, like Threads.

## The command-line client

`araldo` is also a client of the API, from any machine that can reach `/v1`
([ADR 0028](adr/0028-cli-as-api-client.md)), modeled on GitHub's `gh`.

```sh
araldo auth login --hostname araldo.example.com   # a one-time code to approve in the dashboard
araldo auth login --hostname araldo.example.com --live   # ...and live mode beside it
araldo auth login --hostname araldo.example.com --org "Spectrum Labs"   # a default org, if you are in several
araldo auth login --hostname araldo.example.com --with-token < key.txt   # an API key, for scripts and CI
araldo auth status                      # both modes on each server, and whether they still work
araldo channels list                    # a table in a terminal; tab-separated when piped
araldo channels list --live             # the same, in live mode
araldo templates list --brand araldo     # and templates get release --brand araldo: text, data schema, an example
araldo posts list --status needs_attention   # posts, newest first
araldo posts get post_…                  # each channel's copy: status, link, error
araldo posts preview --brand araldo --body "Shipped 1.0"   # every channel's text and problems; fails if any
araldo posts create --brand araldo --template release --data '{"version":"1.0"}' --at next_slot
araldo posts cancel post_…
araldo members list                     # members, invite, role, remove
araldo members invite --email them@example.com --role editor   # prints the link to send them
araldo org view                         # and org update --name
araldo listen --forward-to http://localhost:3000/webhooks   # events as they happen, to your own server
araldo channels list --json handle,status --jq '.[] | select(.status != "active")'
araldo api channels                     # any /v1 request, authenticated
araldo api -X GET posts -f limit=5      # -f fields are the query with -X GET...
araldo api posts --paginate --jq '.data | length'   # every page of a list, as one list
araldo api -X POST posts --input post.json   # ...and otherwise make it a POST, as with gh api
```

- **Signing in is a device code**, as with `gh`: `auth login` prints a
  one-time code like `BDFG-HJKL` and opens the dashboard's **Sign in a
  device** page (or prints its address, over SSH). There, confirm your
  password, type the code, and approve: the page shows the computer's name,
  the mode and where the request came from. The code is always typed,
  never carried in a link, so only a code you just saw in your own terminal
  can be approved. The CLI then gets a **token that acts
  as you**: your role in each of your orgs, read at the time, so a change
  takes effect at once. Sign devices out under **Your account → Devices**
  or with `auth logout`. A token lasts until then, or a year unused;
  changing your password, or an operator's reset, revokes them all. Some changes (API keys,
  owners, no longer requiring two-factor) need a password confirmation and
  stay in the dashboard.
- **In several orgs?** Name one with `--org` (an ID or name), set a default
  with `auth login --org`, or set `ARALDO_ORG`. An org that requires
  two-factor authentication refuses the token of someone without it.
- As the Stripe CLI does, it keeps a **test credential and a live one** for
  each server, since every token and key belongs to one mode. Commands act
  in test mode, which reaches only sandbox channels, unless given `--live`.
- Credentials are kept in the system keychain (macOS Keychain, Windows
  Credential Manager, the Secret Service on Linux), or in `hosts.yaml` in the
  config directory, mode 0600, where there is no keychain or with
  `--insecure-storage`.
- `ARALDO_TOKEN` (a token or an API key, such as one in CI) and
  `ARALDO_HOST` override the stored sign-in; `ARALDO_CONFIG_DIR` moves the
  config directory.
- An API key can do what its scopes allow, but never manage members or the
  org: that takes a person.
- **`araldo listen`**, like `stripe listen`, prints the org's events as they
  happen (`GET /v1/events/stream`) and, with `--forward-to`, POSTs each to
  a local URL signed like a webhook (`Araldo-Signature`), so a receiver can
  be built on a laptop with no public address. The signing secret it
  prints is this computer's and stays the same, so the receiver is set up
  once. `--events` picks types; it reconnects and resumes on its own.

Commands that change the server itself (`migrate`, and under `araldo
admin`: `bootstrap`, `keys`, `users`, `apikeys create`, and the operator's
`members` and `org`) still run where the deployment's configuration is,
with direct access to its database.

## AI assistants (MCP)

`araldo mcp` gives an AI assistant Araldo's tools over the Model Context
Protocol ([ADR 0020](adr/0020-mcp.md)). It talks to an Araldo API with a
key, so it needs no database, and the key decides what the assistant may
do: start with a test key (its posts reach only sandbox channels), then a
live key limited to the brand it should post for. Without `ARALDO_URL` and
`ARALDO_API_KEY` it uses the credential `araldo auth login` stored: the
test one, or the live one with `araldo mcp --live` (and `--org` for a
person in several orgs).

| Tool | Does |
|---|---|
| `list_platforms`, `list_brands`, `list_channels`, `list_templates`, `get_template` | Read what there is (read-only). |
| `upload_media_from_url` | Fetch an image or video for posts to attach. |
| `preview_post` | Render a post per channel and list every rule it breaks (read-only). |
| `create_post` | Schedule it (with an idempotency key, so a retry does not post twice). |
| `list_posts`, `get_post` | Follow up: status, links, errors, engagement (read-only). |
| `cancel_post` | Stop what has not published yet (marked destructive). |
| `retry_target`, `mark_target_published` | Resolve one channel's copy that failed or needs attention: publish it again, or record that it did go out. |
| `reschedule_post` | Move a post to another time or slot, or swap it with another. |
| `engagement_summary` | What did best, by post, channel or template (read-only). |
| `ads_summary` | Ad spend and results, by brand, account, campaign or day (read-only). |
| `analytics_summary` | Visitors and signups by post, network, campaign or day, from the brand's web analytics (read-only). |
| `brand_report` | A brand's month beside the one before: publishing, engagement, web traffic, ads and newsletters (read-only). |
| `preview_newsletter`, `draft_newsletter`, `list_newsletters` | Write and check a newsletter issue and save it as a draft for a person to schedule; follow issues' results. |

Claude Code:

```bash
claude mcp add araldo --env ARALDO_URL=https://araldo.example.com \
  --env ARALDO_API_KEY=ald_test_… -- araldo mcp
```

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "araldo": {
      "command": "araldo",
      "args": ["mcp"],
      "env": { "ARALDO_URL": "https://araldo.example.com", "ARALDO_API_KEY": "ald_test_…" }
    }
  }
}
```

**Over HTTP**, with nothing to install: the server answers MCP at
`POST /v1/mcp` with the same tools, as the key sent with it (scopes, brand
limit and rate limit apply). In Claude Code:

```bash
claude mcp add --transport http araldo https://araldo.example.com/v1/mcp   --header "Authorization: Bearer ald_test_…"
```

Any MCP client that connects to a URL with a header works the same way.
The endpoint is stateless (no session, no server-initiated messages) and
refuses requests a browser makes from another site.

The key needs `brands:read`, `channels:read` and `posts:read`/`posts:write`
(and `templates:read` for templates, `ads:read` for `ads_summary`,
`newsletters:read`/`newsletters:write` for the newsletter tools); a key
with no scopes listed has them all. It does not need, and should not have,
an administrative scope.

## Members and org settings

Members, their roles and the org's settings are for people, not API keys.
In the dashboard, admins invite people from the org page: each invitation
is a one-time link, valid for a week, that the invitee opens to join,
signing in or creating their account. The operator can also use these
server administration commands, run where the
server's configuration is. They act as the operator, with an owner's
permissions, in the org named by `--org` (an ID or name; not needed on a
server with one org), and the audit log records the command, not a member
([ADR 0028](adr/0028-cli-as-api-client.md)):

```bash
araldo admin members list   --org "Spectrum Labs"
araldo admin members add    --org "Spectrum Labs" --email them@example.com --role editor
araldo admin members role   --org "Spectrum Labs" --email them@example.com --role admin
araldo admin members remove --org "Spectrum Labs" --email them@example.com
araldo admin org update     --org "Spectrum Labs" --name "Spectrum Labs" --require-mfa true
```

The rules still hold: an org keeps at least one owner.

**Deleting an org** removes it and everything in it, for good: brands,
channels, posts and their history, templates, media (files in a bucket
too), API keys, webhooks and invitations, and its data key, so its stored
credentials stay unreadable even in old database backups
([ADR 0008](adr/0008-encryption.md)). Members keep their accounts, and the
audit log keeps the record. An owner does it at the bottom of the org's
page, confirming their password and typing its name; the operator, with
`araldo admin org delete --org NAME --confirm NAME`. The old names
(`araldo members`, `araldo org`, and the other commands now under `admin`)
still work for now, and say where they went; `--as` is no longer needed.

`members add` prints a temporary password for someone without an account
(or reads one with `--password-stdin`).

## Hosting orgs for others

An install that hosts orgs for other people (a hosted plan, an agency
running Araldo for its clients) keeps accounts and billing in its own
service, which drives Araldo from outside
([ADR 0031](adr/0031-operator-api.md)). Araldo offers that service an
**operator API**, limits and a status per org, and links out to sign up
and to pay; it knows nothing of plans, prices or payment providers.

**Operator keys** call the operator API, under `/v1/operator/`, and
nothing else; org keys and user tokens cannot call it. Each key is shown
once:

```bash
araldo admin operator-keys create --name "billing service"
araldo admin operator-keys list
araldo admin operator-keys revoke --key opkey_…
```

With one, the service creates an org with `POST /v1/operator/orgs` (a
name, the first owner's email, an optional `external_ref` of its own,
unique on the install, and limits) and sends the first owner the
invitation link in the response; they set their own password and
two-factor authentication in Araldo. It finds an org by `external_ref`,
changes its name, status, note and limits, invites people, reads its
usage for a month, and deletes it. Every change is audited with the key's
ID. The API reference lists the operations under Operator.

**Limits** cap brands, live channels, members (with open invitations) and
live posts a calendar month (UTC); each is unlimited unless set, and
lowering one removes nothing. A member who reaches one is refused with
`limit_reached`, and owners and admins see usage against the limits on
the org page.

**Status** is `active`, `read_only` (members and keys read but change
nothing; scheduled posts still go out) or `suspended` (keys and tokens
are refused, members see only a notice, and nothing is published or
sent; posts whose deadline passes meanwhile are not published). A note
with the status is shown to the org.

The operator can do the same from the command line:

```bash
araldo admin org update --org "Customer" --status read_only --status-note "Your card was declined."
araldo admin org update --org "Customer" --limits brands=3,channels=10,members=5,posts_per_month=500 --external-ref cus_123
```

**Links out.** With `ARALDO_SIGNUP_URL`, the sign-in page offers *Create
an account* there, and orgs come only from the operator. With
`ARALDO_BILLING_URL` and `ARALDO_BILLING_LINK_KEY`, owners see *Billing*,
which sends them to that URL with `token=` added: a hand-off naming the
org (and its `external_ref`), the person and their role, valid for five
minutes. Owners can follow it from a suspended org, which is how one pays
to be restored. The billing service checks it as ADR 0031 describes:

```text
token     = "v1." + payload + "." + signature        (both base64url, unpadded)
payload   = JSON {org, external_ref, user, email, role, iat, exp}
signature = HMAC-SHA256(ARALDO_BILLING_LINK_KEY, "v1." + payload)
```

Compare the signature in constant time, refuse it once `exp` has passed,
and accept each token once.

**Developer apps for every org** ([ADR 0030](adr/0030-install-wide-apps.md),
under Connecting platforms) spare a hosted install's customers registering
their own.

## Media

Images attached to posts ([ADR 0017](adr/0017-media.md)) are stored in
Postgres by default, so server and worker share them with nothing more to
set up, and database backups include them. An install posting many large
images should use S3-compatible storage instead (the `ARALDO_S3_*`
variables): new files go there, files stored earlier stay readable where
they are, and files in a bucket need their own backups. Media no post uses
is deleted after a day (the `media.prune` task); media a post uses is kept
as long as the post. With the Helm chart, put the S3 keys in the
`existingSecret` and the rest in `extraEnv`; the server and the worker
must both have them, since one stores files and the other reads them.

An image too big for a platform, or of a type it does not take, is resized
for it when the post is published ([ADR 0027](adr/0027-video-and-resizing.md)):
scaled down only as far as needed and re-encoded as a JPEG, turned upright
from its EXIF orientation, with the original going unchanged to every
platform it fits. The preview lists these as notices. Araldo never crops: an
image the wrong shape for a platform is still refused. GIFs, images with
transparent pixels and images over 50 megapixels are not resized; they are
refused with the reason. Platforms that fetch images by link get the resized
copy through a link signed for that platform.

**Video** (MP4 or QuickTime) needs S3-compatible storage: uploads through
`POST /v1/media`, or from a URL, stream straight into the bucket, up to
`ARALDO_MAX_VIDEO_BYTES`, and are never held in memory or Postgres. Araldo
reads each video's length, frame rate and codecs from its index and does
not transcode, so export H.264 with AAC in an MP4. Each platform's video
rules are checked as images' are; a platform takes video once its adapter
can post it, and the preview says when one cannot yet. Today Bluesky (through
its video service), X (chunked uploads), Mastodon, Gab, LinkedIn, Facebook
Pages, Instagram (as reels shared to the feed), Threads, YouTube, TikTok,
Telegram and Discord take video; Instagram and Threads fetch it through a signed link, so
the install's API must be public; where a platform processes a video
before posting it, publishing waits for it, up to 20 minutes, renewing its
lease so the wait is not mistaken for a lost worker.

Mastodon and Gab channels need an access token with the `write:media` scope
to post images; one made before images were supported must be replaced.

**Gab.** Gab Social is a Mastodon fork, so a Gab channel works like a
Mastodon one without the server field, and posts can run to 3000 characters.
Two things differ in practice. Gab makes no promise about `Idempotency-Key`,
so Araldo treats it as non-idempotent: an attempt whose outcome is unknown
waits for a person (Posts -> *needs attention*) instead of being retried,
where a Mastodon target would retry itself. And Gab documents no image size
limit, so Araldo checks only the image's type and leaves the size to Gab --
an image it refuses comes back as a publishing failure rather than a rule
violation at preview.

## Engagement

The `engagement.collect` task reads each published post's likes, reposts,
replies and quotes 1 hour, 6 hours, 1, 3, 7 and 30 days after publishing
([ADR 0018](adr/0018-engagement.md)):

| Platform | What is read |
|---|---|
| Bluesky | Likes, reposts, replies and quotes, through its public AppView (no sign-in). |
| Mastodon, Gab | Favourites, boosts, replies and quotes, with the channel's token. Gab reports no quotes. |
| Threads | Views, likes, replies, reposts and quotes. |
| Facebook Pages | Reactions, comments and shares. |
| Instagram | Likes and comments. |
| YouTube | Views, likes and comments. |

X, LinkedIn (members and Pages), Pinterest and TikTok are not read yet,
and Discord and Telegram report nothing: their posts show no engagement.
A count a platform does not report stays zero. Posts published before an upgrade to a version with
engagement are read once soon after it, then on the schedule. A failed
reading is retried later and never affects publishing; see the target's
`engagement.state` and the task on Organization → Background tasks.

## Ads

Araldo reads ad accounts' spend and results so they sit next to organic
engagement, across brands ([ADR 0023](adr/0023-paid-promotion.md)). It
spends nothing: campaigns are made in each network's own tools, and Araldo
reads every campaign in a connected account. Connect accounts on the
dashboard's **Ads** page or with `POST /v1/ad_accounts`; both need the
explicit `ads:write` scope (admins and owners). Reading needs `ads:read`.

The `ads.collect` task reads an account soon after it connects (the last
30 days), then daily, each time re-reading the last 7 days, since networks
revise a day's numbers as late conversions are attributed. Amounts are in
the account's currency, in its minor unit. A network that refuses the
credentials marks the account *needs reauth*; connect it again.

Test mode has a sandbox network with invented numbers.

**Reddit.** At reddit.com/prefs/apps, create a *web app* with Araldo's
redirect URI (`{ARALDO_BASE_URL}/connect/reddit_ads/callback`), and ask
Reddit for Ads API access for it. Add its client ID and secret under
Channels → Developer apps as *Reddit Ads*, then on the Ads page sign in
and choose the ad accounts to read. Araldo asks only for `adsread`, with
a permanent refresh token it swaps for an hour-long access token on each
read. Spend arrives in millionths of the account's currency and is stored
in its minor unit. Results stay 0: they count conversions, which Reddit
measures with its pixel. To count signups, tag ad links with UTM parameters
(`utm_medium=paid`) and read them in your own analytics: Araldo never asks
for a network's tracking pixel.

## Web analytics

Araldo credits visitors and signups to the posts, networks and campaigns
whose tagged links brought them, by reading counts from the brand's own web
analytics ([ADR 0025](adr/0025-web-analytics.md)). It never reads
individual visitors, and adds no tracking script.

1. List the brand's sites under its **UTM domains**: links to them get
   `utm_source` (the network), `utm_medium=social`, `utm_campaign` (the
   template) and `utm_content` (the post's ID). Ad links made with the Ads
   page's builder carry `utm_medium=paid`.
2. Define a goal for what counts as a signup in the analytics tool.
3. Connect the site on the **Performance** page or with
   `POST /v1/analytics_sources`, naming the goals (needs `brands:write`).

**Plausible** (hosted or self-hosted Community Edition): a Stats API key
from Account settings → API keys, the site's domain, and your install's
address if you self-host. The `analytics.collect` task reads the last 30
days at first, then daily, re-reading the last 7 because tools revise
them.

**Google Analytics 4**: the property ID (GA4 → Admin → Property details,
a number, not the `G-` measurement ID) and a service account's JSON key.
Create the service account in a Google Cloud project with the Google
Analytics Data API enabled, add a JSON key, and add the account's email to
the property as a **Viewer** (Admin → Property access management). Goals
are key event names (`sign_up`); tags are the session's. A key Google
refuses, or an account taken off the property, marks the source for
reconnecting. Some Google Workspace organizations forbid service account
keys; use a project outside them.

`GET /v1/analytics/summary` and the MCP tool `analytics_summary`
group the counts by post, source, medium, campaign, content or day; visits
without Araldo's tags are reported as `untagged`.

## Newsletters

Araldo designs, approves and schedules newsletter issues; each mail
account's provider sends them and owns the subscribers, unsubscribes,
bounces and complaints ([ADR 0024](adr/0024-newsletters.md)). Araldo
stores no subscriber address.

1. **Send from a subdomain of its own**, such as `news.example.com`, so
   complaints about newsletters never touch the domain your sign-in and
   receipt mail use. Authenticate it at the provider: its DKIM record, the
   provider in the subdomain's SPF record, and a DMARC record (start with
   `p=none`).
2. **Connect a mail account** on the dashboard's **Newsletters** page or
   with `POST /v1/mail_accounts` (needs `channels:write`): the provider's
   API key and a sender it will send as. Then choose its default
   audiences.
3. **Set the brand's email theme** on its page or with
   `POST /v1/brands/{id}/email_theme`: logo, accent color and the postal
   address anti-spam laws require. An issue cannot be scheduled without
   the address.
4. **Write an issue**, send yourself a test, and schedule it. It follows
   the brand's approval policy. A day before its send time, the
   `newsletters.handoff` task gives each delivery to its provider as a
   scheduled campaign, which then goes out without Araldo; until then it
   can be moved, sent back to draft or canceled, and afterwards moving and
   canceling reach the provider too. `newsletters.read` reads each
   delivery's results 1 hour, 1, 3, 7 and 30 days after it is sent.

**Brevo**: an API key from SMTP & API → API keys. The sender must be an
active Brevo sender or on a domain authenticated there. Audiences are
Brevo's contact lists and segments. Tests go through Brevo's
transactional API.

Links to the brand's UTM domains carry `utm_medium=email`, the provider as
source, the issue's ID as campaign and `link-N` as content, so the
Performance page credits signups to issues. Images from the media library
get a link that never expires, signed for newsletters only; the install
needs its public URL (`ARALDO_BASE_URL`) and master keys for them.

## Reports

The dashboard's **Reports** page, `GET /v1/reports` and the MCP tool
`brand_report` show one brand's period, a calendar month in its time zone
by default, beside the period of the same length before it: posts
published and failed by network, engagement and the top posts, visitors
and signups with the top sources, campaigns and posts, ad spend and cost
per signup per currency, and newsletters sent with their results
([ADR 0026](adr/0026-reports.md)). It is computed when asked for, so it
always has the latest figures; sections a key may not see (ads need
`ads:read`, newsletters `newsletters:read`) or that are empty are left
out. Print the page, or save it as a PDF from the browser, to keep or send
a copy.

**Share links.** Admins and owners can make a link to a brand's month from
its report, for a client or anyone outside the org: anyone holding it reads
that one report, read-only and without an account, for 30 days. It links
nowhere in the dashboard, asks search engines not to index it, and stops
working when withdrawn from the report page, when it expires, or while the
org is suspended. Each link is audited (`report.share`, `report.unshare`);
it is shown once, and only its hash is stored.

## Backups

Araldo has no backup command: back it up the way you back up any Postgres
application ([ADR 0013](adr/0013-backups.md) explains why).

- **The database** holds everything: orgs, channels, templates, posts and
  their history, and media stored in Postgres (the default). Use your
  platform's snapshots, a streaming replica, or continuous archiving
  (pgBackRest, WAL-G); `pg_dump --format=custom --no-owner` works for a
  small install.
- **The bucket**, if media is in S3-compatible storage: turn on versioning,
  or back it up separately.
- **The master keys**, which are not in the database
  ([ADR 0008](adr/0008-encryption.md)): keep them, or the Transit key,
  apart from the database backups and offline. Without them a restored
  database's stored credentials cannot be read, and every channel must be
  connected again.

To restore, load the database into an empty Postgres, point
`ARALDO_DATABASE_URL` at it with the same master keys, and run `araldo
migrate` (or let the server migrate at startup). `araldo admin keys status`
then shows whether every data key can be unwrapped.

## Health

Araldo degrades rather than stops when something it depends on is missing
or down, and recovers on its own:

- **The database is down** (at startup or later): Araldo starts anyway and
  checks Postgres every few seconds. Meanwhile the API answers
  `database_unavailable` (503, `Retry-After`) and the dashboard a short
  503 page, the worker waits, and a startup migration is retried until it
  succeeds. `/readyz` fails only for a server that has not yet found its
  schema current; one that has stays ready, since every pod shares the
  database and taking them all out of the load balancer would replace
  Araldo's 503 with the balancer's own error. A malformed `ARALDO_DATABASE_URL` still stops
  it: that needs fixing, not waiting out.
- **The master keys are unavailable** (none configured, or their Transit
  service down): everything that needs no stored credential works.
  Connecting channels, publishing to them, and signed media links fail with
  `keys_unavailable` (503) and are retried; the log says when the keys
  become unavailable and when they recover. A malformed master key still
  stops Araldo.

- `GET /healthz`: the process is up.
- `GET /readyz`: the schema is current (checked against the database; once
  it has been, a database outage does not make the server unready).
- The dashboard's **Organization → Background tasks** shows every periodic
  task, its last success and failures.

## Metrics

`araldo server`, `araldo worker` and `araldo all` export OpenTelemetry
metrics, configured only through the standard `OTEL_*` variables
([ADR 0014](adr/0014-telemetry.md)). Nothing is exported until one is set.

| Variable | Meaning |
|---|---|
| `OTEL_METRICS_EXPORTER` | `prometheus` serves a scrape endpoint; `otlp` pushes to a collector; `console`; `none`. |
| `OTEL_EXPORTER_PROMETHEUS_HOST`, `OTEL_EXPORTER_PROMETHEUS_PORT` | Where `/metrics` listens, default `localhost:9464`. Use `0.0.0.0` for scrapes from other hosts. It is a separate listener: the public port never serves metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` (or `…_METRICS_ENDPOINT`), `OTEL_EXPORTER_OTLP_PROTOCOL` | The collector for `otlp`; setting an endpoint alone also turns OTLP export on. |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | Resource attributes (default `service.name=araldo`). |
| `OTEL_SDK_DISABLED=true` | Turns everything off. |

### What is measured

Names as Prometheus shows them. Labels are bounded: never an org, brand,
user, channel, post or target ID, never post text, and errors only by kind,
never by message. `mode` is `live` or `test`.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `araldo_post_targets` | gauge | `status`, `mode` | Post targets in each status (`queued`, `publishing`, `held`, `needs_attention`, `failed`, `published`, `canceled`), zeros included. |
| `araldo_post_targets_due` | gauge | `mode` | Queued targets the publisher could claim now: due, on an active channel not held by a rate limit. |
| `araldo_post_targets_oldest_due_age_seconds` | gauge | `mode` | How long the oldest of those has been due; 0 when none is. |
| `araldo_task_last_success_timestamp_seconds` | gauge | `task` | Unix time of each background task's last successful run. |
| `araldo_task_consecutive_failures` | gauge | `task` | Failures of each task since its last success. |
| `araldo_publish_attempts_total` | counter | `provider`, `mode`, `outcome`, `error_kind` | Publish attempts. `outcome` is what the publisher did: `published`, `retry`, `rate_limited`, `needs_attention`, `failed`. `error_kind` is the platform's classification: `none`, `rate_limited`, `auth_revoked`, `transient`, `uncertain`, `rejected`, `unknown`. |
| `araldo_webhook_delivery_attempts_total` | counter | `mode`, `outcome` | Webhook delivery attempts: `succeeded`, `retry`, `failed` (gave up after three days). |
| `araldo_task_failures_total` | counter | `task` | Failed background task runs (errors, panics, timeouts). |

The gauges are read from the database when metrics are collected (one
grouped count of targets and the task table, at most every 10 seconds per
process) and cover every org. Every process reports the same values, so
aggregate them with `max`, not `sum`. The counters count what each process
did: publishing and deliveries happen in workers, task failures in whichever
worker holds the task's lease, so `sum` them.

### Helm chart

All off by default:

| Value | Effect |
|---|---|
| `metrics.enabled` | Sets `OTEL_METRICS_EXPORTER=prometheus` and the port (`metrics.port`, default 9464) on the server and worker, and declares a `metrics` container port. The Service does not expose it. |
| `metrics.podMonitor.enabled` | A `monitoring.coreos.com/v1` PodMonitor scraping both roles (`interval`, `scrapeTimeout`, `labels` for your Prometheus's selector). Needs `metrics.enabled`. |
| `metrics.prometheusRule.enabled` | A `monitoring.coreos.com/v1` PrometheusRule with the alerts below. `labels` go on the object, `alertLabels` on every alert; `dashboardURL` (default `config.baseURL`) is where runbook links point; `selector` overrides the PromQL matchers that pick this release's series (default: the release namespace, and the PodMonitor's job when it is on). |

Each alert under `metrics.prometheusRule.alerts` has `enabled`, `for`,
`severity` and its thresholds:

| Alert | Fires when (defaults) | Runbook link |
|---|---|---|
| `AraldoPostNeedsAttention` (warning) | `max(araldo_post_targets{mode="live", status="needs_attention"}) > 0` for 5m. | Home, where live mode counts targets needing attention. Open each post, check the account, then **Retry** or **Mark published** ([ADR 0011](adr/0011-publishing.md)). |
| `AraldoPublishFailing` (warning) | Per provider, over `window` (30m), at least `minFailures` (3) live attempts ended in `retry`, `failed` or `needs_attention`, and they are at least `ratio` (0.5) of that provider's live attempts; for 15m. Rate limits do not count. | Posts: each post's attempt history shows the platform's error; Channels shows accounts to reconnect. |
| `AraldoPublishingStalled` (critical) | The oldest claimable live target has been due over `dueSeconds` (900), or `publish.reclaim` last succeeded over `taskStaleSeconds` (600) ago; for 5m. The worker is down, stuck or cannot reach the database. | Organization → Background tasks. |
| `AraldoBackgroundTaskFailing` (warning) | `max by (task) (araldo_task_consecutive_failures) >= consecutiveFailures` (3) for 5m. | Organization → Background tasks, with the last error. |

The alerts cannot see a deployment with no running pods at all; alert on
the scrape targets themselves (`up`) for that.

## Kubernetes

The chart is in `deploy/helm/araldo` and published to
`oci://ghcr.io/spectrum-labs-tech/charts/araldo`: `0.0.0-main` follows the
main branch (its appVersion pins the exact image), and `X.Y.Z` follows
release tags. It needs `existingSecret` (a Secret with `ARALDO_DATABASE_URL`
and `ARALDO_MASTER_KEYS`, unless `transit` holds the master key) and
`config.baseURL`. With `transit.enabled` and a `transit.role`, each pod gets
a projected ServiceAccount token for the role's audience and logs in with it.

**Node drains evict pods one at a time** for each role with more than one
replica (a PodDisruptionBudget, `podDisruptionBudget.enabled`). With one
replica there is no budget, since it would block the drain: run two servers
if a drain must not interrupt the API.

**Rate limits are per server pod.** The API's limit per key (25 requests a
second, bursts of 100) and the sign-in limit per IP are kept in each pod's
memory, so with N server pods a client that spreads its requests can get up
to N times as many, and the `RateLimit-*` headers describe one pod. The
per-account sign-in lockout is in the database and counts across all pods.

**Migrations run before the rollout.** A `pre-install`/`pre-upgrade` hook Job
runs `araldo migrate`; only when it succeeds does Helm update the Deployments.
If it fails, the upgrade stops, the running pods keep serving the previous
version on the previous schema, the release is marked `failed`, and the Job is
kept so `kubectl logs job/<release>-migrate` shows why. The pods do not
migrate, and `/readyz` stays unready until the schema is current. Every
migration works with the previous version, which keeps serving until the new
pods are ready, and never blocks writes for long ([ADR 0029](adr/0029-backward-compatible-migrations.md)):
one that cannot get a lock within 5 seconds fails, and the upgrade can be
retried. Rendering the
chart without hooks (`helm template | kubectl apply`)? Set
`migrations.job=false` and the pods migrate at startup instead.
