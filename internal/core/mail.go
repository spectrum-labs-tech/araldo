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

// mailHTML is the layout of every email the install sends: Araldo's look
// (the dashboard's accent and warm greys) in tables and inline styles, as
// mail clients need. The mark is drawn in a table cell, not an image, so it
// shows where clients block images. Light only: a client that darkens mail
// keeps the contrast, as every pair here is AA or better.
var mailHTML = template.Must(template.New("mail").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta name="color-scheme" content="light"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:24px 12px;background:#f7f6f3;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#1d1b16">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;margin:0 auto"><tr><td>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 16px"><tr>
<td width="28" height="28" align="center" valign="middle" style="width:28px;height:28px;background:#8a3b12;border-radius:6px;color:#ffffff;font-weight:700;font-size:17px;line-height:28px">A</td>
<td style="padding-left:10px;font-weight:700;font-size:18px;color:#1d1b16">Araldo</td>
</tr></table>
<div style="background:#ffffff;border:1px solid #e4e0d6;border-radius:10px;padding:32px">
{{range .Paragraphs}}<p style="margin:0 0 16px;font-size:16px;line-height:1.6">{{.}}</p>
{{end}}{{if .ButtonURL}}<p style="margin:24px 0"><a href="{{.ButtonURL}}" style="display:inline-block;background:#8a3b12;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:8px;font-weight:600">{{.ButtonLabel}}</a></p>
<p style="margin:0 0 16px;font-size:13px;color:#5f5a4e;word-break:break-all">Or open this link: <a href="{{.ButtonURL}}" style="color:#8a3b12">{{.ButtonURL}}</a></p>
{{end}}</div>
{{with .Footer}}<p style="margin:16px 4px 0;font-size:13px;line-height:1.5;color:#5f5a4e">{{.}}</p>{{end}}
</td></tr></table></body></html>
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
