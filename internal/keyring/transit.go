// SPDX-License-Identifier: AGPL-3.0-or-later

package keyring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultServiceAccountTokenFile is where Kubernetes projects a pod's
// ServiceAccount token.
const DefaultServiceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // G101: a path, not a credential

// TransitConfig reaches a key held by a Transit secrets engine (OpenBao or
// Vault), which wraps and unwraps data keys without ever handing out the
// master key.
type TransitConfig struct {
	// Addr is the server, e.g. http://openbao:8200.
	Addr string
	// Mount is the Transit engine's path (default "transit").
	Mount string
	// Key is the Transit key's name.
	Key string
	// Token is a static token. Without one, Role logs in through the
	// Kubernetes (or JWT) auth method with the pod's ServiceAccount token.
	Token string
	// Role is the auth method role to log in as.
	Role string
	// AuthPath is the auth method's mount (default "kubernetes").
	AuthPath string
	// JWTFile is the ServiceAccount token (default
	// DefaultServiceAccountTokenFile). It is read at every login, since
	// Kubernetes rotates it.
	JWTFile string
	// HTTP is the client (default: one with a 30-second timeout).
	HTTP *http.Client
}

// Transit is a KEK held by a Transit engine.
type Transit struct {
	cfg  TransitConfig
	http *http.Client
	now  func() time.Time

	mu      sync.Mutex
	token   string
	refresh time.Time
	login   *login // the login under way, if any
}

// login is one login to the service, which every caller needing a token
// meanwhile waits for, each only as long as its own context allows.
type login struct {
	done  chan struct{}
	token string
	err   error
}

// NewTransit returns a KEK using the Transit key in cfg.
func NewTransit(cfg TransitConfig) (*Transit, error) {
	if cfg.Addr == "" || cfg.Key == "" {
		return nil, errors.New("keyring: Transit needs an address and a key name")
	}
	if cfg.Token == "" && cfg.Role == "" {
		return nil, errors.New("keyring: Transit needs a token or a role to log in as")
	}
	cfg.Addr = strings.TrimRight(cfg.Addr, "/")
	cfg.Mount = strings.Trim(cmp(cfg.Mount, "transit"), "/")
	cfg.AuthPath = strings.Trim(cmp(cfg.AuthPath, "kubernetes"), "/")
	cfg.JWTFile = cmp(cfg.JWTFile, DefaultServiceAccountTokenFile)
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Transit{cfg: cfg, http: client, now: time.Now, token: cfg.Token}, nil
}

func cmp(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ID names the key, as recorded on the data keys it wraps.
func (t *Transit) ID() string { return "transit:" + t.cfg.Mount + "/" + t.cfg.Key }

// Wrap encrypts plaintext under the Transit key. Transit has no associated
// data, so the plaintext is prefixed with a hash of aad, which Unwrap
// checks: a wrapped key moved to another scope or version fails to open.
func (t *Transit) Wrap(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	sum := sha256.Sum256(aad)
	bound := append(sum[:], plaintext...)
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	body := map[string]string{"plaintext": base64.StdEncoding.EncodeToString(bound)}
	if err := t.call(ctx, "encrypt", body, &out); err != nil {
		return nil, err
	}
	if out.Data.Ciphertext == "" {
		return nil, errors.New("transit: empty ciphertext")
	}
	return []byte(out.Data.Ciphertext), nil
}

// Unwrap decrypts what Wrap returned for the same aad.
func (t *Transit) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := t.call(ctx, "decrypt", map[string]string{"ciphertext": string(wrapped)}, &out); err != nil {
		return nil, err
	}
	bound, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("transit: plaintext: %w", err)
	}
	sum := sha256.Sum256(aad)
	if len(bound) < len(sum) || subtle.ConstantTimeCompare(bound[:len(sum)], sum[:]) != 1 {
		return nil, ErrDecrypt
	}
	return bound[len(sum):], nil
}

// call posts to the Transit key's encrypt or decrypt endpoint. A rejected
// token is renewed once: after a server restart, a token that looked valid
// is gone.
func (t *Transit) call(ctx context.Context, op string, body, out any) error {
	path := "/v1/" + t.cfg.Mount + "/" + op + "/" + url.PathEscape(t.cfg.Key)
	for attempt := 0; ; attempt++ {
		token, err := t.currentToken(ctx)
		if err != nil {
			return unavailable(err)
		}
		status, err := t.post(ctx, path, token, body, out)
		if status == http.StatusForbidden && attempt == 0 && t.cfg.Token == "" {
			t.forget()
			continue
		}
		// A rejected ciphertext is a broken secret; anything else (no
		// answer, a server error, a refused token) is the service being
		// unavailable.
		if err != nil && status != http.StatusBadRequest {
			return unavailable(err)
		}
		return err
	}
}

// currentToken returns the token to call with, logging in when there is
// none or it is due for renewal. Callers arriving during a login wait for
// it rather than each logging in, and none holds the lock across the call.
func (t *Transit) currentToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	if t.token != "" && (t.cfg.Token != "" || t.now().Before(t.refresh)) {
		tok := t.token
		t.mu.Unlock()
		return tok, nil
	}
	l := t.login
	if l == nil {
		l = &login{done: make(chan struct{})}
		t.login = l
		// The login outlives a caller that gives up, so those still
		// waiting get its token; the HTTP client's timeout bounds it.
		go func() {
			tok, refresh, err := t.logIn(context.WithoutCancel(ctx))
			t.mu.Lock()
			if err == nil {
				t.token, t.refresh = tok, refresh
			}
			t.login = nil
			t.mu.Unlock()
			l.token, l.err = tok, err
			close(l.done)
		}()
	}
	t.mu.Unlock()
	select {
	case <-l.done:
		return l.token, l.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// logIn exchanges the service account token for a token, and says when to
// renew it.
func (t *Transit) logIn(ctx context.Context) (string, time.Time, error) {
	jwt, err := os.ReadFile(t.cfg.JWTFile)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("transit: service account token: %w", err)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	body := map[string]string{"role": t.cfg.Role, "jwt": strings.TrimSpace(string(jwt))}
	if _, err := t.post(ctx, "/v1/auth/"+t.cfg.AuthPath+"/login", "", body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("transit: log in as %q: %w", t.cfg.Role, err)
	}
	if out.Auth.ClientToken == "" {
		return "", time.Time{}, errors.New("transit: login returned no token")
	}
	// Log in again at half the lease, well before the token expires.
	lease := time.Duration(out.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = time.Minute
	}
	return out.Auth.ClientToken, t.now().Add(lease / 2), nil
}

func (t *Transit) forget() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = ""
}

func (t *Transit) post(ctx context.Context, path, token string, body, out any) (int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.Addr+path, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("transit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("transit: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("transit: %s %s: %d %s", http.MethodPost, path, resp.StatusCode, strings.Join(e.Errors, "; "))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("transit: response: %w", err)
	}
	return resp.StatusCode, nil
}
