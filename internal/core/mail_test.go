// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

func TestMailMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		c        mailContent
		wantHTML []string
		notHTML  []string
		wantText []string
	}{
		{
			name: "with a button and footer",
			c: mailContent{To: "a@example.com", Subject: "Join Acme on Araldo", Paragraphs: []string{"Ann invited you."},
				ButtonLabel: "Accept the invitation", ButtonURL: "https://araldo.example/invite/x", Footer: "You got this because Ann invited you."},
			wantHTML: []string{"<title>Join Acme on Araldo</title>", `background:#8a3b12;border-radius:6px`, ">Araldo</td>",
				`<a href="https://araldo.example/invite/x" style="display:inline-block;background:#8a3b12;color:#ffffff`,
				"Accept the invitation</a>", "You got this because Ann invited you."},
			notHTML:  []string{"#1d4ed8"},
			wantText: []string{"Ann invited you.\n\n", "Accept the invitation: https://araldo.example/invite/x\n\n", "-- \nYou got this"},
		},
		{
			name:     "paragraphs are escaped, and no button means no link line",
			c:        mailContent{To: "a@example.com", Subject: "Hi", Paragraphs: []string{"<b>Acme</b> & co"}},
			wantHTML: []string{"&lt;b&gt;Acme&lt;/b&gt; &amp; co"},
			notHTML:  []string{"<b>Acme", "Or open this link"},
			wantText: []string{"<b>Acme</b> & co\n\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := tt.c.message()
			if err != nil {
				t.Fatal(err)
			}
			if m.To != tt.c.To || m.Subject != tt.c.Subject {
				t.Errorf("to %q, subject %q", m.To, m.Subject)
			}
			for _, w := range tt.wantHTML {
				if !strings.Contains(m.HTML, w) {
					t.Errorf("HTML lacks %s", w)
				}
			}
			for _, w := range tt.notHTML {
				if strings.Contains(m.HTML, w) {
					t.Errorf("HTML has %s", w)
				}
			}
			for _, w := range tt.wantText {
				if !strings.Contains(m.Text, w) {
					t.Errorf("text lacks %q", w)
				}
			}
		})
	}
}
