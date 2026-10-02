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
	Discord   Provider = "discord"
	Telegram  Provider = "telegram"
	X         Provider = "x"
	Facebook  Provider = "facebook"
	Instagram Provider = "instagram"
	Threads   Provider = "threads"
	LinkedIn  Provider = "linkedin"
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
	// Open returns the file. Rule checks leave it nil.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

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
	return fmt.Sprintf("image%d%s", i+1, media.Extension(m.Type))
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

func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Msg != "" {
		msg += ": " + e.Msg
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
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
