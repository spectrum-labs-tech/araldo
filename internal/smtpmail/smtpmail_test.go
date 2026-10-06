// SPDX-License-Identifier: AGPL-3.0-or-later

package smtpmail_test

import (
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail/smtptest"
)

func client(t *testing.T, srv *smtptest.Server, mode, user, pass string) *smtpmail.Client {
	t.Helper()
	c, err := smtpmail.New(smtpmail.Config{Host: srv.Host, Port: srv.Port, Username: user, Password: pass,
		From: "Araldo <noreply@araldo.example>", TLS: mode})
	if err != nil {
		t.Fatal(err)
	}
	c.TLSConfig = srv.ClientTLS()
	return c
}

var msg = smtpmail.Message{To: "Ada <ada@araldo.example>", Subject: "Réinitialiser your password", Text: "Open the link.\n",
	HTML: "<p>Open the <a href=\"https://araldo.example/x\">link</a>.</p>", Headers: map[string]string{"List-Unsubscribe": "<https://araldo.example/u>"}}

func TestSendInEachMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{smtptest.Plain, smtptest.StartTLS, smtptest.Implicit} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			srv := smtptest.New(t, smtptest.Options{Mode: mode, Username: "user", Password: "pass"})
			if err := client(t, srv, mode, "user", "pass").Send(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			got := srv.Mail()
			if len(got) != 1 {
				t.Fatalf("received %d messages", len(got))
			}
			m := got[0]
			if m.From != "noreply@araldo.example" || len(m.To) != 1 || m.To[0] != "ada@araldo.example" || m.Auth != "user" || m.TLS != (mode != smtptest.Plain) {
				t.Fatalf("envelope: %+v", m)
			}
			h := m.Message.Header
			subject, _ := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
			if subject != msg.Subject || h.Get("List-Unsubscribe") != "<https://araldo.example/u>" || h.Get("Message-Id") == "" ||
				!strings.Contains(h.Get("From"), "noreply@araldo.example") {
				t.Fatalf("headers: %v", h)
			}
			_, params, err := mime.ParseMediaType(h.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			mr := multipart.NewReader(m.Message.Body, params["boundary"])
			var parts []string
			for {
				p, err := mr.NextPart() // decodes quoted-printable
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(p)
				parts = append(parts, p.Header.Get("Content-Type")+"|"+string(body))
			}
			if len(parts) != 2 || parts[0] != "text/plain; charset=utf-8|"+msg.Text || parts[1] != "text/html; charset=utf-8|"+msg.HTML {
				t.Fatalf("parts: %q", parts)
			}
		})
	}
}

func TestSendFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		mode      string // the server's
		client    string // the client's
		pass      string
		reject    string
		msg       smtpmail.Message
		permanent bool
	}{
		{name: "wrong password", mode: smtptest.StartTLS, client: smtpmail.StartTLS, pass: "nope", msg: msg, permanent: true},
		{name: "unknown recipient", mode: smtptest.StartTLS, client: smtpmail.StartTLS, pass: "pass", reject: "550 no such user", msg: msg, permanent: true},
		{name: "try later", mode: smtptest.StartTLS, client: smtpmail.StartTLS, pass: "pass", reject: "451 try later", msg: msg},
		{name: "no STARTTLS offered", mode: smtptest.Plain, client: smtpmail.StartTLS, pass: "pass", msg: msg},
		{name: "a header with a line break", mode: smtptest.StartTLS, client: smtpmail.StartTLS, pass: "pass",
			msg: smtpmail.Message{To: "ada@araldo.example", Subject: "x", Text: "x", Headers: map[string]string{"X-A": "a\r\nBcc: eve@evil.example"}}, permanent: true},
		{name: "a bad recipient", mode: smtptest.StartTLS, client: smtpmail.StartTLS, pass: "pass",
			msg: smtpmail.Message{To: "ada@araldo.example\r\nBcc: eve@evil.example", Subject: "x", Text: "x"}, permanent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := smtptest.New(t, smtptest.Options{Mode: tc.mode, Username: "user", Password: "pass", RejectRcpt: tc.reject})
			err := client(t, srv, tc.client, "user", tc.pass).Send(t.Context(), tc.msg)
			if err == nil || smtpmail.IsPermanent(err) != tc.permanent {
				t.Fatalf("err = %v, permanent %v, want permanent %v", err, smtpmail.IsPermanent(err), tc.permanent)
			}
			if len(srv.Mail()) != 0 {
				t.Fatal("a message was received")
			}
		})
	}
}

func TestNewChecksTheConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  smtpmail.Config
		ok   bool
	}{
		{"fine", smtpmail.Config{Host: "smtp.example", From: "noreply@araldo.example"}, true},
		{"no host", smtpmail.Config{From: "noreply@araldo.example"}, false},
		{"bad from", smtpmail.Config{Host: "smtp.example", From: "not an address"}, false},
		{"bad port", smtpmail.Config{Host: "smtp.example", Port: 70000, From: "noreply@araldo.example"}, false},
		{"bad mode", smtpmail.Config{Host: "smtp.example", From: "noreply@araldo.example", TLS: "ssl"}, false},
	} {
		if _, err := smtpmail.New(tc.cfg); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
