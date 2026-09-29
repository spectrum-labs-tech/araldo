// SPDX-License-Identifier: AGPL-3.0-or-later

package id

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Vectors from the TypeID specification's valid.yml.
func TestFormatMatchesSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		uuid string
		want string
	}{
		{"00000000-0000-0000-0000-000000000000", "00000000000000000000000000"},
		{"00000000-0000-0000-0000-000000000001", "00000000000000000000000001"},
		{"00000000-0000-0000-0000-000000000020", "00000000000000000000000010"},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", "7zzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"01890a5d-ac96-774b-bcce-b302099a8057", "01h455vb4pex5vsknk084sn02q"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			u := uuid.MustParse(tt.uuid)
			got := Format(Post, u)
			if got != "post_"+tt.want {
				t.Fatalf("Format = %q, want post_%s", got, tt.want)
			}
			back, err := Parse(Post, got)
			if err != nil {
				t.Fatalf("Parse(%q): %v", got, err)
			}
			if back != u {
				t.Fatalf("round trip = %s, want %s", back, u)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()
	good := Format(Post, New())
	tests := map[string]string{
		"wrong prefix":      Format(Template, New()),
		"no prefix":         good[len("post_"):],
		"too short":         good[:len(good)-1],
		"too long":          good + "0",
		"overflow":          "post_8zzzzzzzzzzzzzzzzzzzzzzzzz",
		"excluded letter i": "post_0000000000000000000000000i",
		"uppercase":         "post_01H455VB4PEX5VSKNK084SN02Q",
		"empty":             "",
	}
	for name, s := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(Post, s); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalid", s, err)
			}
		})
	}
}

func TestNewIsTimeOrdered(t *testing.T) {
	t.Parallel()
	a, b := Make(Event), Make(Event)
	if a >= b {
		t.Fatalf("IDs not increasing: %q then %q", a, b)
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("0123456789abcdef"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) != 16 {
			return
		}
		u := uuid.UUID(b)
		back, err := Parse(Brand, Format(Brand, u))
		if err != nil || back != u {
			t.Fatalf("round trip of %x = %s, %v", b, back, err)
		}
	})
}
