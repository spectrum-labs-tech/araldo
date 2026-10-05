// SPDX-License-Identifier: AGPL-3.0-or-later

// Package platform defines what Araldo knows about each social platform
// (ADR 0009): its rules, how to connect to it, and how to publish. Each
// platform is an Adapter in a subpackage; the sandbox adapter imitates any
// of them in test mode.
package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/media"
)

// Provider names a platform.
type Provider string

// Providers Araldo knows.
const (
	Sandbox   Provider = "sandbox"
	Bluesky   Provider = "bluesky"
	Mastodon  Provider = "mastodon"
	Gab       Provider = "gab"
	Discord   Provider = "discord"
	Telegram  Provider = "telegram"
	X         Provider = "x"
	Facebook  Provider = "facebook"
	Instagram Provider = "instagram"
	Threads   Provider = "threads"
	LinkedIn  Provider = "linkedin"
	// LinkedInPages posts as company Pages, through its own app (the
	// Community Management API); LinkedIn posts to a member's feed.
	LinkedInPages Provider = "linkedin_pages"
	Pinterest     Provider = "pinterest"
	YouTube       Provider = "youtube"
	TikTok        Provider = "tiktok"
)

// Credentials are a channel's decrypted settings and secrets, by field
// name (see Adapter.Fields).
type Credentials map[string]string

// Field describes one setting a channel needs to connect.
type Field struct {
	Name     string
	Label    string
	Help     string
	Secret   bool
	Optional bool
	Default  string
}

// Account is who a channel posts as.
type Account struct {
	ExternalID  string
	Handle      string
	DisplayName string
	URL         string
}

// RemoteRef identifies one published item on a platform.
type RemoteRef struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
	// Extra holds what a platform needs to reply to the item (Bluesky's
	// URI and CID, for example).
	Extra map[string]string `json:"extra,omitempty"`
}

// Payload is what one target publishes.
type Payload struct {
	// Key is stable across retries of the same target, for platforms that
	// accept an idempotency key or caller-chosen record keys. KeyTime is
	// when the target was created, stable too.
	Key     string
	KeyTime time.Time
	// Attempt counts publish attempts for this target, from 1.
	Attempt int
	// Parts is the text: one part, or several for a thread.
	Parts []string
	// Posted holds parts already published by an earlier attempt, in
	// order; publishing continues after them.
	Posted []RemoteRef
	// Simulate asks the sandbox to imitate a failure (ADR 0006). Real
	// adapters ignore it.
	Simulate string
	// Media goes on the first part (ADR 0017). An adapter resuming after
	// the first part has nothing to upload.
	Media []Media
}

// Media is an image attached to a post (ADR 0017).
type Media struct {
	// Type is the MIME type, read from the file itself.
	Type          string
	Size          int64
	Width, Height int
	Alt           string
	// Transparent images are never resized into a JPEG (ADR 0027).
	Transparent bool
	// A video's length, frames per second and codecs (ADR 0027).
	Duration   time.Duration
	FrameRate  float64
	VideoCodec string
	AudioCodec string
	// URL is a public, expiring link to the file, for platforms that fetch
	// images themselves (ADR 0021); empty when the install cannot sign one.
	URL string
	// Open returns the file. Rule checks leave it nil.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// IsVideo reports whether m is a video.
func (m Media) IsVideo() bool { return media.IsVideo(m.Type) }

// WithData returns m serving data, which also sets its size.
func (m Media) WithData(data []byte) Media {
	m.Size = int64(len(data))
	m.Open = func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return m
}

// Read returns the whole file, or a Transient error if it cannot be read
// (storage is ours, not the platform's, so trying again may work).
func (m Media) Read(ctx context.Context) ([]byte, error) {
	if m.Open == nil {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: "the image is not available"}
	}
	rc, err := m.Open(ctx)
	if err != nil {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: "reading the image", Err: err}
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, m.Size+1))
	if err != nil {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: "reading the image", Err: err}
	}
	if int64(len(b)) != m.Size {
		return nil, &Error{Kind: Transient, Code: "media_unreadable", Msg: fmt.Sprintf("the image has %d bytes, not %d", len(b), m.Size)}
	}
	return b, nil
}

// Filename is a name for an upload, for platforms that want one.
func (m Media) Filename(i int) string {
	kind := "image"
	if m.IsVideo() {
		kind = "video"
	}
	return fmt.Sprintf("%s%d%s", kind, i+1, media.Extension(m.Type))
}

// Result is a successful publish.
type Result struct {
	Parts     []RemoteRef
	Permalink string
}

// Adapter connects to and publishes on one platform.
type Adapter interface {
	Provider() Provider
	Rules() Rules
	// Fields lists what a channel must provide to connect.
	Fields() []Field
	// Verify checks credentials and returns the account they post as.
	Verify(ctx context.Context, c Credentials) (Account, error)
	// Publish posts p. It calls onPart after each part succeeds, so a
	// thread interrupted midway can resume. An error is a *Error.
	Publish(ctx context.Context, c Credentials, p Payload, onPart func(RemoteRef) error) (Result, error)
	// Idempotent reports whether retrying a Payload with the same Key can
	// never publish twice; then an uncertain attempt is simply retried.
	Idempotent() bool
}

// Kind classifies a failure; the publisher acts on it (ADR 0011).
type Kind string

// Failure kinds.
const (
	RateLimited Kind = "rate_limited"
	AuthRevoked Kind = "auth_revoked"
	Rejected    Kind = "rejected"
	Transient   Kind = "transient"
	Uncertain   Kind = "uncertain"
)

// Error is a classified platform failure.
type Error struct {
	Kind       Kind
	RetryAfter time.Duration
	// Code is a short machine-readable reason (for example "too_long").
	Code string
	Msg  string
	Err  error
}

// Error is the error's text, scrubbed of credentials (see Scrub): it is
// stored on targets, sent in events and logged.
func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Msg != "" {
		msg += ": " + e.Msg
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return Scrub(msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf returns a classified error.
func Errorf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// KindOf returns err's kind; an unclassified error counts as Uncertain,
// the safe assumption (ADR 0011).
func KindOf(err error) Kind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return Uncertain
}

// Registry holds the available adapters.
type Registry struct {
	adapters map[Provider]Adapter
}

// NewRegistry returns a registry of adapters.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: map[Provider]Adapter{}}
	for _, a := range adapters {
		r.adapters[a.Provider()] = a
	}
	return r
}

// Get returns the adapter for p.
func (r *Registry) Get(p Provider) (Adapter, bool) {
	a, ok := r.adapters[p]
	return a, ok
}

// Providers lists the registered providers, sorted, without the sandbox.
func (r *Registry) Providers() []Provider {
	var out []Provider
	for p := range r.adapters {
		if p != Sandbox {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Counts is a post's engagement (ADR 0018). Views is nil where the
// platform does not report it.
type Counts struct {
	Likes   int64  `json:"likes"`
	Reposts int64  `json:"reposts"`
	Replies int64  `json:"replies"`
	Quotes  int64  `json:"quotes"`
	Views   *int64 `json:"views,omitempty"`
}

// Total is likes, reposts, replies and quotes together.
func (c Counts) Total() int64 { return c.Likes + c.Reposts + c.Replies + c.Quotes }

// Add returns c plus o; views stay unknown unless one side knows them.
func (c Counts) Add(o Counts) Counts {
	out := Counts{Likes: c.Likes + o.Likes, Reposts: c.Reposts + o.Reposts, Replies: c.Replies + o.Replies, Quotes: c.Quotes + o.Quotes}
	if c.Views != nil || o.Views != nil {
		v := deref(c.Views) + deref(o.Views)
		out.Views = &v
	}
	return out
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// EngagementReader is implemented by adapters whose platform reports
// engagement (ADR 0018).
type EngagementReader interface {
	// Engagement returns the counts of each ref the platform still has, by
	// RemoteRef.ID; a ref missing from the result was deleted. An error is
	// a *Error.
	Engagement(ctx context.Context, c Credentials, refs []RemoteRef) (map[string]Counts, error)
}

// App is a developer app's credentials (ADR 0021).
type App struct {
	ClientID     string
	ClientSecret string
}

// Connection is an account a sign-in returned, ready to become a channel.
type Connection struct {
	Account     Account     `json:"account"`
	Credentials Credentials `json:"credentials"`
	// ExpiresAt is when the token expires, if it does.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Connector is implemented by adapters whose channels connect with OAuth
// (ADR 0021).
type Connector interface {
	// AuthorizeURL is where the member signs in and grants access.
	// challenge is a PKCE S256 challenge, for platforms that take one.
	AuthorizeURL(app App, redirectURI, state, challenge string) string
	// Exchange turns the code the platform sent back (and the PKCE
	// verifier) into the accounts the member can connect.
	Exchange(ctx context.Context, app App, redirectURI, code, verifier string) ([]Connection, error)
}

// Refresher is implemented by adapters whose tokens expire and can be
// renewed. An error is a *Error, AuthRevoked meaning the member must sign
// in again now; or ErrNoRefresh, for a token that cannot be renewed but
// still works until it expires.
type Refresher interface {
	// Refresh returns the credentials that changed (the new tokens); the
	// channel keeps the rest (a Page or board ID, an API version).
	Refresh(ctx context.Context, app App, c Credentials) (Credentials, *time.Time, error)
}
