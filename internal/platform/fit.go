// SPDX-License-Identifier: AGPL-3.0-or-later

package platform

import (
	"regexp"
	"strings"
)

// Fit says what to do with text longer than a platform allows (ADR 0010).
type Fit string

// Fit modes.
const (
	FitError    Fit = "error"
	FitTruncate Fit = "truncate"
	FitThread   Fit = "thread"
)

// ThreadBreak separates thread parts in rendered text; templates insert it
// with {{thread}}.
const ThreadBreak = "\x1e"

// Valid reports whether f is a known mode.
func (f Fit) Valid() bool { return f == FitError || f == FitTruncate || f == FitThread }

const ellipsis = "…"

var tokenRE = regexp.MustCompile(`\S+|\s+`)

// Split turns rendered text into parts: explicit thread breaks first, then
// the fit mode for any part that is still too long. It never fails; Check
// the result for what could not be fixed.
func (r Rules) Split(text string, mode Fit) []string {
	var parts []string
	for p := range strings.SplitSeq(text, ThreadBreak) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if r.Length(p) <= r.MaxLength {
			parts = append(parts, p)
			continue
		}
		switch {
		case mode == FitTruncate:
			parts = append(parts, r.truncate(p))
		case mode == FitThread && r.Threads:
			parts = append(parts, r.pack(p)...)
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

// truncate shortens s at a word boundary, ending with an ellipsis.
func (r Rules) truncate(s string) string {
	tokens := tokenRE.FindAllString(s, -1)
	for len(tokens) > 0 {
		cand := strings.TrimRight(strings.Join(tokens, ""), " \t\r\n.,;:") + ellipsis
		if r.Length(cand) <= r.MaxLength {
			return cand
		}
		tokens = tokens[:len(tokens)-1]
	}
	return r.hardCut(s, ellipsis)
}

// pack splits s into parts that each fit, breaking between words.
func (r Rules) pack(s string) []string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if p := strings.TrimSpace(cur.String()); p != "" {
			parts = append(parts, p)
		}
		cur.Reset()
	}
	for _, tok := range tokenRE.FindAllString(s, -1) {
		if r.Length(strings.TrimSpace(cur.String()+tok)) <= r.MaxLength {
			cur.WriteString(tok)
			continue
		}
		flush()
		if strings.TrimSpace(tok) == "" {
			continue
		}
		for r.Length(tok) > r.MaxLength { // a single word longer than a part
			head := r.hardCut(tok, "")
			parts = append(parts, head)
			tok = tok[len(head):]
		}
		cur.WriteString(tok)
	}
	flush()
	return parts
}

// hardCut returns the longest prefix of s that fits with suffix appended.
func (r Rules) hardCut(s, suffix string) string {
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if r.Length(string(runes[:mid])+suffix) <= r.MaxLength {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(runes[:lo]) + suffix
}
