// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"errors"
	"net/url"
	"regexp"
)

// Platform errors end up where people and other systems read them: a
// target's error message, events and webhooks, logs. Some platforms carry
// credentials in the request URL (Meta's and Threads' access_token query
// parameter; a Discord webhook's token in its path), and Go's transport
// errors quote the whole URL. So the URL is dropped from transport errors,
// and every error's text is scrubbed of anything token-shaped.

// redactURL drops the URL from a transport error, keeping what failed. The
// caller names the host.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &transportError{op: ue.Op, err: ue.Err}
	}
	return err
}

type transportError struct {
	op  string
	err error
}

func (e *transportError) Error() string { return e.op + ": " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

var (
	secretParam   = regexp.MustCompile(`(?i)\b((?:access|refresh|id)_token|client_secret|token|key|signature|sig|password)=[^&\s"'\\]+`)
	discordHook   = regexp.MustCompile(`(?i)(discord(?:app)?\.com/api/(?:v\d+/)?webhooks/\d+/)[A-Za-z0-9_.-]+`)
	telegramToken = regexp.MustCompile(`/bot\d+:[A-Za-z0-9_-]+`)
)

// Scrub replaces credentials in text with REDACTED: token-like query
// parameters, Discord webhook tokens and Telegram bot tokens.
func Scrub(s string) string {
	s = secretParam.ReplaceAllString(s, "${1}=REDACTED")
	s = discordHook.ReplaceAllString(s, "${1}REDACTED")
	return telegramToken.ReplaceAllString(s, "/botREDACTED")
}
