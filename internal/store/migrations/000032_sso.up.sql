SET LOCAL lock_timeout = '5s';

-- OIDC single sign-on (ADR 0033).

-- An org's identity provider. The client secret is sealed with the org's
-- data key (ADR 0008).
CREATE TABLE sso_connections (
    org_id        uuid        NOT NULL PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    issuer        text        NOT NULL,
    client_id     text        NOT NULL,
    client_secret bytea       NOT NULL,
    default_role  text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Email domains an org signs in through its provider, once a DNS TXT
-- record proves it holds them. A domain is verified for one org at most.
CREATE TABLE sso_domains (
    org_id      uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    domain      text        NOT NULL,
    token       text        NOT NULL,
    verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, domain)
);
CREATE UNIQUE INDEX sso_domains_verified_key ON sso_domains (domain) WHERE verified_at IS NOT NULL;

-- A sign-in sent to the provider, until it comes back: single use.
CREATE TABLE sso_states (
    state_hash bytea       NOT NULL PRIMARY KEY,
    org_id     uuid        NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    verifier   text        NOT NULL,
    nonce      text        NOT NULL,
    next       text        NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL
);

-- Whether an org is reached only through its single sign-on, and which org
-- a session or CLI token signed in through.
ALTER TABLE orgs ADD COLUMN require_sso boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD COLUMN sso_org_id uuid;
ALTER TABLE device_authorizations ADD COLUMN sso_org_id uuid;
ALTER TABLE user_tokens ADD COLUMN sso_org_id uuid;
