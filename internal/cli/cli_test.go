// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no command prints usage", args: nil, wantCode: ExitUsage, wantStderr: "Usage: araldo"},
		{name: "help", args: []string{"help"}, wantCode: ExitOK, wantStdout: "version"},
		{name: "--help", args: []string{"--help"}, wantCode: ExitOK, wantStdout: "Usage: araldo"},
		{name: "version", args: []string{"version"}, wantCode: ExitOK, wantStdout: "araldo dev\n"},
		{name: "version rejects arguments", args: []string{"version", "extra"}, wantCode: ExitUsage, wantStderr: "takes no arguments"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: ExitUsage, wantStderr: `unknown command "frobnicate"`},
		{name: "apikeys needs a subcommand", args: []string{"apikeys"}, wantCode: ExitUsage, wantStderr: "apikeys create"},
		{name: "apikeys unknown subcommand", args: []string{"apikeys", "list"}, wantCode: ExitUsage, wantStderr: `unknown apikeys command "list"`},
		{name: "apikeys create needs email and name", args: []string{"apikeys", "create", "--email", "a@example.com"}, wantCode: ExitUsage, wantStderr: "--email and --name are required"},
		{name: "members needs a subcommand", args: []string{"members"}, wantCode: ExitUsage, wantStderr: "members list | add | role | remove"},
		{name: "members add needs a target", args: []string{"members", "add", "--as", "a@example.com", "--role", "editor"}, wantCode: ExitUsage, wantStderr: "--email"},
		{name: "members role needs a real role", args: []string{"members", "role", "--as", "a@example.com", "--email", "b@example.com", "--role", "boss"}, wantCode: ExitUsage, wantStderr: "--role is owner, admin, editor or viewer"},
		{name: "org needs update", args: []string{"org"}, wantCode: ExitUsage, wantStderr: "org update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			code := Run(t.Context(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantStdout == "" && stdout.Len() > 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestPickOrg(t *testing.T) {
	t.Parallel()
	a := model.Membership{OrgID: uuid.New(), OrgName: "Spectrum Labs"}
	b := model.Membership{OrgID: uuid.New(), OrgName: "Other"}
	tests := []struct {
		name      string
		ms        []model.Membership
		org       string
		want      uuid.UUID
		wantUsage bool
		wantErr   bool
	}{
		{name: "the only org", ms: []model.Membership{a}, want: a.OrgID},
		{name: "by name, ignoring case", ms: []model.Membership{a, b}, org: "spectrum labs", want: a.OrgID},
		{name: "several orgs need --org", ms: []model.Membership{a, b}, wantUsage: true, wantErr: true},
		{name: "no orgs", ms: nil, wantUsage: true, wantErr: true},
		{name: "unknown name", ms: []model.Membership{a, b}, org: "Nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := pickOrg(tt.ms, tt.org)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if errors.Is(err, errUsage) != tt.wantUsage {
				t.Errorf("usage error = %v, want %v", errors.Is(err, errUsage), tt.wantUsage)
			}
			if err == nil && m.OrgID != tt.want {
				t.Errorf("org = %v, want %v", m.OrgID, tt.want)
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: "posts:write", want: []string{"posts:write"}},
		{in: " posts:write, templates:write ,,", want: []string{"posts:write", "templates:write"}},
	}
	for _, tt := range tests {
		if got := splitList(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("splitList(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrintKeyUsage(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		use     []keyring.KeyUse
		want    []string
		wantErr bool
	}{
		"moved to Transit": {
			use: []keyring.KeyUse{
				{ID: "transit:transit/araldo", Configured: true, Primary: true, DataKeys: 4},
				{ID: "k1", Configured: true},
			},
			want: []string{"transit:transit/araldo", "primary", "k1", "can be removed"},
		},
		"rewrap pending": {
			use: []keyring.KeyUse{
				{ID: "transit:transit/araldo", Configured: true, Primary: true, DataKeys: 1},
				{ID: "k1", Configured: true, DataKeys: 3},
			},
			want: []string{"still in use: run araldo keys rotate"},
		},
		"a key removed too early": {
			use: []keyring.KeyUse{
				{ID: "transit:transit/araldo", Configured: true, Primary: true, DataKeys: 1},
				{ID: "k1", DataKeys: 3},
			},
			want:    []string{"NOT CONFIGURED"},
			wantErr: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := printKeyUsage(&out, tt.use)
			if (err != nil) != tt.wantErr {
				t.Fatalf("printKeyUsage error = %v, want error %v", err, tt.wantErr)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
		})
	}
}

// Without a URL and key, or a stored sign-in, mcp says how to get one. It
// points the config directory at an empty one, so a developer's own
// sign-in cannot satisfy it, and so cannot run in parallel.
func TestMCPNeedsACredential(t *testing.T) {
	t.Setenv("ARALDO_CONFIG_DIR", t.TempDir())
	for _, k := range []string{"ARALDO_URL", "ARALDO_API_KEY", "ARALDO_HOST", "ARALDO_TOKEN"} {
		t.Setenv(k, "")
	}
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"mcp"}, &stdout, &stderr); code != ExitUsage ||
		!strings.Contains(stderr.String(), "araldo auth login") || !strings.Contains(stderr.String(), "ARALDO_API_KEY") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
