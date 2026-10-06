// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
)

// The install's own email (ADR 0034).

var errMailOff = &apperr.Error{Kind: apperr.KindUnavailable, Code: "mail_unavailable",
	Message: "This server does not send email. Ask an owner of your org, or the server's operator, to reset your password."}

// MailEnabled reports whether the install sends email.
func (s *Service) MailEnabled() bool { return s.cfg.Mail != nil }

// mailContent is one email's words: paragraphs, and a button.
type mailContent struct {
	To          string
	Subject     string
	Paragraphs  []string
	ButtonLabel string
	ButtonURL   string
	// Footer is small print under the button: why they got it.
	Footer string
}

var mailHTML = template.Must(template.New("mail").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:24px;background:#f5f5f4;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#1c1917">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e7e5e4;border-radius:8px;padding:32px">
<p style="margin:0 0 24px;font-weight:600;font-size:18px">Araldo</p>
{{range .Paragraphs}}<p style="margin:0 0 16px;font-size:16px;line-height:1.5">{{.}}</p>
{{end}}{{if .ButtonURL}}<p style="margin:24px 0"><a href="{{.ButtonURL}}" style="display:inline-block;background:#1d4ed8;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:6px;font-weight:600">{{.ButtonLabel}}</a></p>
<p style="margin:0 0 16px;font-size:13px;color:#57534e;word-break:break-all">Or open this link: {{.ButtonURL}}</p>
{{end}}{{with .Footer}}<p style="margin:24px 0 0;font-size:13px;color:#57534e">{{.}}</p>{{end}}
</div></body></html>
`))

// message renders an email as text and HTML.
func (c mailContent) message() (smtpmail.Message, error) {
	var text strings.Builder
	for _, p := range c.Paragraphs {
		text.WriteString(p + "\n\n")
	}
	if c.ButtonURL != "" {
		text.WriteString(c.ButtonLabel + ": " + c.ButtonURL + "\n\n")
	}
	if c.Footer != "" {
		text.WriteString("-- \n" + c.Footer + "\n")
	}
	var html bytes.Buffer
	if err := mailHTML.Execute(&html, c); err != nil {
		return smtpmail.Message{}, err
	}
	return smtpmail.Message{To: c.To, Subject: c.Subject, Text: text.String(), HTML: html.String()}, nil
}
