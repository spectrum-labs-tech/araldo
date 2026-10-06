// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smtpmail sends the install's own email to its users over SMTP
// (ADR 0034): password resets and notifications. It is not how newsletters
// go out; those use each brand's mail account (package email).
package smtpmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// TLS modes.
const (
	// StartTLS upgrades a plain connection (port 587) and refuses a server
	// that cannot.
	StartTLS = "starttls"
	// ImplicitTLS speaks TLS from the start (port 465).
	ImplicitTLS = "tls"
	// NoTLS sends in the clear: only for a relay on the same host or
	// network, such as a local MTA. Credentials are then refused unless the
	// server is localhost (net/smtp's PlainAuth).
	NoTLS = "none"
)

// Config is an SMTP server and the address mail comes from.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the sender, as "Araldo <noreply@araldo.example>" or a bare
	// address.
	From string
	// TLS is StartTLS, ImplicitTLS or NoTLS; empty picks ImplicitTLS on
	// port 465 and StartTLS otherwise.
	TLS string
}

// Message is one email to one person.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// Headers are added as given (List-Unsubscribe, for one); a name or
	// value with a line break is refused.
	Headers map[string]string
}

// Sender sends email.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Client sends through one SMTP server.
type Client struct {
	cfg  Config
	from *mail.Address
	// TLSConfig is used for TLS; nil checks the server's certificate
	// against the system's roots for Host. Tests replace it.
	TLSConfig *tls.Config
	// Timeout bounds a send when the context has no deadline.
	Timeout time.Duration
}

// New checks cfg and returns a client for it.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("an SMTP host is required")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("the from address %q is not valid: %w", cfg.From, err)
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("SMTP port %d is not valid", cfg.Port)
	}
	switch cfg.TLS {
	case "":
		cfg.TLS = StartTLS
		if cfg.Port == 465 {
			cfg.TLS = ImplicitTLS
		}
	case StartTLS, ImplicitTLS, NoTLS:
	default:
		return nil, fmt.Errorf("SMTP TLS mode %q is not starttls, tls or none", cfg.TLS)
	}
	return &Client{cfg: cfg, from: from, Timeout: 30 * time.Second}, nil
}

// From is the sender's address.
func (c *Client) From() string { return c.from.Address }

// PermanentError is a refusal the server says will not change on retry (a
// 5xx reply): an unknown recipient, a message refused.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether retrying err is pointless.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// classify marks a 5xx reply as permanent.
func classify(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return &PermanentError{Err: err}
	}
	return err
}

// Send delivers m.
func (c *Client) Send(ctx context.Context, m Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return &PermanentError{Err: fmt.Errorf("the recipient %q is not valid: %w", m.To, err)}
	}
	raw, err := c.build(to, m)
	if err != nil {
		return &PermanentError{Err: err}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(c.Timeout)
	}
	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	d := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if c.cfg.TLS == ImplicitTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: c.tlsConfig()}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	cl, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return classify(err)
	}
	defer func() { _ = cl.Close() }()
	if c.cfg.TLS == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS; set the TLS mode to none only for a local relay")
		}
		if err := cl.StartTLS(c.tlsConfig()); err != nil {
			return classify(err)
		}
	}
	if c.cfg.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)); err != nil {
			return classify(fmt.Errorf("signing in to the SMTP server: %w", err))
		}
	}
	if err := cl.Mail(c.from.Address); err != nil {
		return classify(err)
	}
	if err := cl.Rcpt(to.Address); err != nil {
		return classify(err)
	}
	w, err := cl.Data()
	if err != nil {
		return classify(err)
	}
	if _, err := w.Write(raw); err != nil {
		return classify(err)
	}
	if err := w.Close(); err != nil {
		return classify(err)
	}
	return classify(cl.Quit())
}

func (c *Client) tlsConfig() *tls.Config {
	if c.TLSConfig != nil {
		return c.TLSConfig
	}
	return &tls.Config{ServerName: c.cfg.Host, MinVersion: tls.VersionTLS12}
}

// build writes the message: headers, then the text and HTML as
// alternatives, quoted-printable.
func (c *Client) build(to *mail.Address, m Message) ([]byte, error) {
	var b bytes.Buffer
	header := func(name, value string) error {
		if strings.ContainsAny(name, "\r\n:") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("the header %q has a line break", name)
		}
		fmt.Fprintf(&b, "%s: %s\r\n", name, value)
		return nil
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	domain := c.from.Address[strings.LastIndexByte(c.from.Address, '@')+1:]
	mw := multipart.NewWriter(&b)
	for _, h := range [][2]string{
		{"From", c.from.String()},
		{"To", to.String()},
		{"Subject", mime.QEncoding.Encode("utf-8", m.Subject)},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`},
	} {
		if err := header(h[0], h[1]); err != nil {
			return nil, err
		}
	}
	for name, value := range m.Headers {
		if err := header(name, value); err != nil {
			return nil, err
		}
	}
	b.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		if part.body == "" {
			continue
		}
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
