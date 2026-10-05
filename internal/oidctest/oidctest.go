// SPDX-License-Identifier: AGPL-3.0-or-later

// Package oidctest is an OpenID Connect provider for tests (ADR 0033): a
// TLS httptest server with discovery, keys and a token endpoint, that signs
// in whoever a test says, without a browser.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Provider is a running test provider with one client.
type Provider struct {
	ClientID     string
	ClientSecret string
	server       *httptest.Server
	key          *rsa.PrivateKey

	mu     sync.Mutex
	grants map[string]grant
}

type grant struct {
	claims      map[string]any
	challenge   string
	redirectURI string
}

// New starts a provider for t, closed when t ends.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{ClientID: "araldo-" + random(), ClientSecret: random(), key: key, grants: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("POST /token", p.token)
	p.server = httptest.NewTLSServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// Issuer is the provider's issuer URL.
func (p *Provider) Issuer() string { return p.server.URL }

// Client reaches the provider (it trusts its certificate).
func (p *Provider) Client() *http.Client { return p.server.Client() }

// Authorize is the person signing in at the provider: given the URL a
// sign-in sent them to, it checks the request and returns the state and
// code the provider sends back. The ID token will carry claims, over the
// standard ones (iss, aud, sub, nonce, iat, exp), which they may replace.
func (p *Provider) Authorize(authURL string, claims map[string]any) (state, code string, err error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	switch {
	case !strings.HasPrefix(authURL, p.server.URL+"/authorize?"):
		return "", "", errors.New("not this provider's authorization endpoint")
	case q.Get("client_id") != p.ClientID:
		return "", "", errors.New("unknown client_id")
	case q.Get("response_type") != "code":
		return "", "", errors.New("response_type is not code")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return "", "", errors.New("no S256 code challenge")
	case !strings.Contains(" "+q.Get("scope")+" ", " openid "):
		return "", "", errors.New("no openid scope")
	case q.Get("state") == "" || q.Get("nonce") == "":
		return "", "", errors.New("no state or nonce")
	}
	all := map[string]any{"iss": p.Issuer(), "aud": p.ClientID, "sub": random(), "nonce": q.Get("nonce"),
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	for k, v := range claims {
		all[k] = v
	}
	code = random()
	p.mu.Lock()
	p.grants[code] = grant{claims: all, challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	p.mu.Unlock()
	return q.Get("state"), code, nil
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer": p.Issuer(), "authorization_endpoint": p.Issuer() + "/authorize", "token_endpoint": p.Issuer() + "/token",
		"jwks_uri": p.Issuer() + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
		"n": b64(p.key.N.Bytes()), "e": b64(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

// token redeems a code once, for the client, with the PKCE verifier.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	g, found := p.grants[code]
	delete(p.grants, code)
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || r.PostForm.Get("grant_type") != "authorization_code" || b64(sum[:]) != g.challenge ||
		r.PostForm.Get("redirect_uri") != g.redirectURI {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	idToken, err := p.sign(g.claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": random(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

// sign makes an RS256 JWT of claims.
func (p *Provider) sign(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b64(b)
}
