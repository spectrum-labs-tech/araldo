// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package core_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// keyedMailer is a live provider with a secret key: it accepts the keys in
// good, refuses the ones in revoked as revoked, and records the key each
// call used. The rest is the sandbox's.
type keyedMailer struct {
	email.SandboxMailer
	mu      *sync.Mutex
	good    map[string]bool
	revoked map[string]bool
	used    *[]string
}

func newKeyedMailer(good ...string) keyedMailer {
	k := keyedMailer{mu: &sync.Mutex{}, good: map[string]bool{}, revoked: map[string]bool{}, used: &[]string{}}
	for _, g := range good {
		k.good[g] = true
	}
	return k
}

func (keyedMailer) Provider() email.Provider { return "keyed" }
func (keyedMailer) Name() string             { return "Keyed" }
func (keyedMailer) Fields() []platform.Field {
	return []platform.Field{{Name: "api_key", Label: "API key", Secret: true}}
}

func (k keyedMailer) check(c platform.Credentials) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	*k.used = append(*k.used, c["api_key"])
	switch {
	case k.revoked[c["api_key"]]:
		return &platform.Error{Kind: platform.AuthRevoked, Code: "revoked", Msg: "the key was revoked"}
	case !k.good[c["api_key"]]:
		return &platform.Error{Kind: platform.Rejected, Code: "bad_key", Msg: "the key is wrong"}
	}
	return nil
}

func (k keyedMailer) Verify(_ context.Context, c platform.Credentials, from email.Address) (email.Account, error) {
	if err := k.check(c); err != nil {
		return email.Account{}, err
	}
	return email.Account{ExternalID: "domain-" + from.Email, Name: "araldo.dev"}, nil
}

func (k keyedMailer) Audiences(_ context.Context, c platform.Credentials) ([]email.Audience, error) {
	if err := k.check(c); err != nil {
		return nil, err
	}
	return email.SandboxAudiences, nil
}

func (k keyedMailer) set(good, revoked string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	clear(k.good) // in place: the registry holds a copy of k
	clear(k.revoked)
	k.good[good] = true
	if revoked != "" {
		k.revoked[revoked] = true
	}
}

func (k keyedMailer) last() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return (*k.used)[len(*k.used)-1]
}

// senderCopy is the key in a sender's own copy of its connection's
// credentials, which the release before connections reads.
func senderCopy(t *testing.T, orgID, accountID uuid.UUID) string {
	t.Helper()
	st := open(t)
	var sealed []byte
	if err := st.Pool().QueryRow(t.Context(), `SELECT credentials FROM mail_accounts WHERE id = $1`, accountID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	mk, err := keyring.ParseMasterKeys(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := keyring.New(mk, st).Decrypt(t.Context(), orgID, keyring.AAD("mail_accounts", "credentials", accountID), sealed)
	if err != nil {
		t.Fatal(err)
	}
	var secrets map[string]string
	if err := json.Unmarshal(raw, &secrets); err != nil {
		t.Fatal(err)
	}
	return secrets["api_key"]
}

func withMailer(m email.Mailer) option {
	return func(cfg *core.Config, _ *[]platform.Adapter) { cfg.Mailers = append(cfg.Mailers, m) }
}

// Senders share a connection's key: a second sender, on another brand,
// takes the connection instead of the key; replacing the key is one edit
// every sender then uses; a refused key marks every sender; a connection
// in use cannot be deleted, and the last sender takes it with it.
func TestMailConnectionsShareAKey(t *testing.T) {
	t.Parallel()
	keyed := newKeyedMailer("key-1")
	w := newWorld(t, withMailer(keyed))
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	other, err := w.s.CreateBrand(ctx, w.owner, core.BrandInput{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, Provider: "keyed", FromName: "News",
		FromEmail: "news@araldo.dev", Fields: map[string]string{"api_key": "key-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConnectionID == nil {
		t.Fatal("a sender connected with a key has no connection")
	}
	conn, err := w.s.MailConnection(ctx, live, *first.ConnectionID)
	if err != nil || conn.Provider != "keyed" || conn.Name != "Keyed: araldo.dev" || conn.Senders != 1 || conn.Status != model.ConnectionActive {
		t.Fatalf("its connection: %+v, %v", conn, err)
	}

	// A second sender takes the connection, not the key.
	if _, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: other.ID, ConnectionID: &conn.ID, Provider: "keyed",
		FromName: "Updates", FromEmail: "updates@araldo.dev"}); apperr.As(err).Code != "connection_conflict" {
		t.Fatalf("a connection and a provider: %v", err)
	}
	second, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: other.ID, ConnectionID: &conn.ID, FromName: "Updates",
		FromEmail: "updates@araldo.dev", DefaultAudiences: []string{"list-2"}})
	if err != nil || second.ConnectionID == nil || *second.ConnectionID != conn.ID || keyed.last() != "key-1" {
		t.Fatalf("a second sender: %+v, %v", second, err)
	}
	if conns, err := w.s.MailConnections(ctx, live); err != nil || len(conns) != 1 || conns[0].Senders != 2 {
		t.Fatalf("connections: %+v, %v", conns, err)
	}
	if as, err := w.s.MailConnectionAudiences(ctx, live, conn.ID); err != nil || len(as) != len(email.SandboxAudiences) {
		t.Fatalf("the connection's audiences: %v, %v", as, err)
	}

	// A key the provider refuses changes nothing; a good one serves both.
	keyed.set("key-2", "")
	if _, err := w.s.UpdateMailConnection(ctx, live, conn.ID, core.MailConnectionUpdate{Fields: map[string]string{"api_key": "wrong"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a wrong key: %v", err)
	}
	name := "Araldo's Keyed account"
	if conn, err = w.s.UpdateMailConnection(ctx, live, conn.ID, core.MailConnectionUpdate{Name: &name,
		Fields: map[string]string{"api_key": "key-2"}}); err != nil || conn.Name != name {
		t.Fatalf("replacing the key: %+v, %v", conn, err)
	}
	for _, ma := range []*model.MailAccount{first, second} {
		if _, err := w.s.MailAudiences(ctx, live, ma.ID); err != nil || keyed.last() != "key-2" {
			t.Fatalf("%s after the new key: %v, used %q", ma.FromEmail, err, keyed.last())
		}
		// The release before connections, rolled back to, sends with it too.
		if got := senderCopy(t, w.org.ID, ma.ID); got != "key-2" {
			t.Errorf("%s's own copy holds %q", ma.FromEmail, got)
		}
	}

	// A revoked key marks the connection and every sender on it.
	keyed.set("key-3", "key-2")
	if _, err := w.s.MailAudiences(ctx, live, first.ID); err == nil {
		t.Fatal("a revoked key read audiences")
	}
	if c, _ := w.s.MailConnection(ctx, live, conn.ID); c.Status != model.ConnectionNeedsReauth {
		t.Errorf("the connection after a revoked key: %s", c.Status)
	}
	if ma, _ := w.s.MailAccount(ctx, live, second.ID); ma.Status != model.ConnectionNeedsReauth {
		t.Errorf("the other sender after a revoked key: %s", ma.Status)
	}
	if _, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: other.ID, ConnectionID: &conn.ID, FromName: "x",
		FromEmail: "x@araldo.dev"}); apperr.As(err).Code != "connection_needs_reauth" {
		t.Errorf("a sender on a refused connection: %v", err)
	}
	if _, err := w.s.UpdateMailConnection(ctx, live, conn.ID, core.MailConnectionUpdate{Fields: map[string]string{"api_key": "key-3"}}); err != nil {
		t.Fatal(err)
	}
	if ma, _ := w.s.MailAccount(ctx, live, second.ID); ma.Status != model.ConnectionActive {
		t.Errorf("the other sender after a new key: %s", ma.Status)
	}

	// A key limited to one brand sees its brand's connections and changes none.
	limited := live
	limited.BrandID = &other.ID
	if conns, err := w.s.MailConnections(ctx, limited); err != nil || len(conns) != 1 {
		t.Errorf("a brand key's connections: %d, %v", len(conns), err)
	}
	if _, err := w.s.UpdateMailConnection(ctx, limited, conn.ID, core.MailConnectionUpdate{Name: &name}); kind(err) != apperr.KindForbidden {
		t.Errorf("a brand key renaming a connection: %v", err)
	}

	// In use, a connection stays; its last sender takes it along.
	if err := w.s.DeleteMailConnection(ctx, live, conn.ID); apperr.As(err).Code != "mail_connection_in_use" {
		t.Fatalf("deleting a connection in use: %v", err)
	}
	if err := w.s.DeleteMailAccount(ctx, live, second.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := w.s.MailConnection(ctx, live, conn.ID); err != nil || c.Senders != 1 {
		t.Fatalf("after one sender left: %+v, %v", c, err)
	}
	if err := w.s.DeleteMailAccount(ctx, live, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.MailConnection(ctx, live, conn.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("after the last sender left: %v", err)
	}
}

// A connection made on its own is checked by reading audiences; a sender
// then joins it.
func TestCreateMailConnection(t *testing.T) {
	t.Parallel()
	keyed := newKeyedMailer("key-1")
	w := newWorld(t, withMailer(keyed))
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	if _, err := w.s.CreateMailConnection(ctx, live, core.MailConnectionInput{Provider: "keyed", Fields: map[string]string{"api_key": "nope"}}); kind(err) != apperr.KindInvalid {
		t.Fatalf("a wrong key: %v", err)
	}
	if _, err := w.s.CreateMailConnection(ctx, w.owner, core.MailConnectionInput{Provider: "keyed", Fields: map[string]string{"api_key": "key-1"}}); apperr.As(err).Code != "livemode_required" {
		t.Fatalf("a live provider in test mode: %v", err)
	}
	limited := live
	limited.BrandID = &w.brand.ID
	if _, err := w.s.CreateMailConnection(ctx, limited, core.MailConnectionInput{Provider: "keyed", Fields: map[string]string{"api_key": "key-1"}}); kind(err) != apperr.KindForbidden {
		t.Fatalf("a brand key making a connection: %v", err)
	}
	c, err := w.s.CreateMailConnection(ctx, live, core.MailConnectionInput{Provider: "keyed", Fields: map[string]string{"api_key": "key-1"}})
	if err != nil || c.Name != "Keyed" || c.Senders != 0 {
		t.Fatalf("create: %+v, %v", c, err)
	}
	ma, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, ConnectionID: &c.ID, FromName: "News", FromEmail: "news@araldo.dev"})
	if err != nil || ma.Provider != "keyed" || ma.Name != "araldo.dev" {
		t.Fatalf("a sender on it: %+v, %v", ma, err)
	}
	// Test mode does not see a live connection.
	if _, err := w.s.MailConnection(ctx, w.owner, c.ID); kind(err) != apperr.KindNotFound {
		t.Fatalf("a live connection in test mode: %v", err)
	}
	unknown := uuid.New()
	if _, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, ConnectionID: &unknown, FromName: "News",
		FromEmail: "other@araldo.dev"}); apperr.As(err).Code != "connection_unknown" {
		t.Fatalf("an unknown connection: %v", err)
	}
}

// A sender the release before connections made (no connection, its key
// sealed for itself) is adopted when first read: a connection with its ID
// and its key, still sealed as it was, which then serves new senders.
func TestMailAccountsAreAdopted(t *testing.T) {
	t.Parallel()
	keyed := newKeyedMailer("key-1")
	w := newWorld(t, withMailer(keyed))
	ctx := t.Context()
	live := w.owner
	live.Livemode = true
	old, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, Provider: "keyed", FromName: "News",
		FromEmail: "news@araldo.dev", Fields: map[string]string{"api_key": "key-1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Make it as the previous release left it: no connection.
	pool := open(t).Pool()
	if _, err := pool.Exec(ctx, `UPDATE mail_accounts SET connection_id = NULL WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM mail_connections WHERE id = $1`, *old.ConnectionID); err != nil {
		t.Fatal(err)
	}

	ma, err := w.s.MailAccount(ctx, live, old.ID)
	if err != nil || ma.ConnectionID == nil || *ma.ConnectionID != old.ID {
		t.Fatalf("adopted: %+v, %v", ma, err)
	}
	if _, err := w.s.MailAudiences(ctx, live, old.ID); err != nil || keyed.last() != "key-1" {
		t.Fatalf("the adopted key: %v, used %q", err, keyed.last())
	}
	c, err := w.s.MailConnection(ctx, live, old.ID)
	if err != nil || c.Senders != 1 || c.Name != "Keyed: araldo.dev" {
		t.Fatalf("the adopted connection: %+v, %v", c, err)
	}
	// It serves a new sender, and its key can be replaced.
	if _, err := w.s.ConnectMailAccount(ctx, live, core.MailAccountInput{BrandID: w.brand.ID, ConnectionID: &c.ID, FromName: "Updates",
		FromEmail: "updates@araldo.dev"}); err != nil {
		t.Fatal(err)
	}
	keyed.set("key-2", "")
	if _, err := w.s.UpdateMailConnection(ctx, live, c.ID, core.MailConnectionUpdate{Fields: map[string]string{"api_key": "key-2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.MailAudiences(ctx, live, old.ID); err != nil || keyed.last() != "key-2" {
		t.Fatalf("after replacing the adopted key: %v, used %q", err, keyed.last())
	}
}
