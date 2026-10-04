// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("ARALDO_DATABASE_URL", "postgres://x")
	t.Setenv("ARALDO_MASTER_KEYS", "k:abc")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.example/")
	t.Setenv("ARALDO_AUTO_MIGRATE", "false")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://araldo.example" || c.AutoMigrate || c.Listen != ":8080" {
		t.Fatalf("config %+v", c)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	t.Setenv("ARALDO_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ARALDO_MASTER_KEYS", "")
	t.Setenv("ARALDO_MASTER_KEYS_FILE", "")
	t.Setenv("ARALDO_BASE_URL", "not a url")
	t.Setenv("ARALDO_INSECURE_COOKIES", "maybe")
	_, err := Load()
	if err == nil {
		t.Fatal("Load succeeded")
	}
	for _, want := range []string{"ARALDO_DATABASE_URL", "ARALDO_BASE_URL", "ARALDO_INSECURE_COOKIES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	// One-off commands need no master keys.
	t.Setenv("ARALDO_DATABASE_URL", "postgres://x")
	t.Setenv("ARALDO_BASE_URL", "")
	t.Setenv("ARALDO_INSECURE_COOKIES", "")
	if _, err := Load(); err != nil {
		t.Fatalf("Load(): %v", err)
	}
}

func TestLoadS3(t *testing.T) {
	t.Setenv("ARALDO_DATABASE_URL", "postgres://x")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.example")
	c, err := Load()
	if err != nil || c.S3.Bucket != "" {
		t.Fatalf("without S3: %+v, %v", c.S3, err)
	}
	t.Setenv("ARALDO_S3_BUCKET", "media")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ARALDO_S3_ENDPOINT") {
		t.Fatalf("a bucket alone: %v", err)
	}
	t.Setenv("ARALDO_S3_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("ARALDO_S3_ACCESS_KEY_ID", "ak")
	t.Setenv("ARALDO_S3_SECRET_ACCESS_KEY", "sk")
	c, err = Load()
	if err != nil || c.S3.Region != "auto" || c.S3.Prefix != "media/" || c.S3.Bucket != "media" {
		t.Fatalf("with S3: %+v, %v", c.S3, err)
	}
}

func TestLoadTransit(t *testing.T) {
	t.Setenv("ARALDO_DATABASE_URL", "postgres://x")
	t.Setenv("ARALDO_BASE_URL", "https://araldo.example")
	t.Setenv("ARALDO_MASTER_KEYS", "")
	t.Setenv("ARALDO_MASTER_KEYS_FILE", "")
	tests := map[string]struct {
		env  map[string]string
		want string // a substring of the error; empty for success
	}{
		"a token": {
			env: map[string]string{"ARALDO_TRANSIT_ADDR": "http://bao:8200", "ARALDO_TRANSIT_KEY": "araldo", "ARALDO_TRANSIT_TOKEN": "t"},
		},
		"a role": {
			env: map[string]string{"ARALDO_TRANSIT_ADDR": "http://bao:8200", "ARALDO_TRANSIT_KEY": "araldo", "ARALDO_TRANSIT_ROLE": "araldo"},
		},
		"no key name": {
			env:  map[string]string{"ARALDO_TRANSIT_ADDR": "http://bao:8200", "ARALDO_TRANSIT_ROLE": "araldo"},
			want: "ARALDO_TRANSIT_KEY",
		},
		"no way to log in": {
			env:  map[string]string{"ARALDO_TRANSIT_ADDR": "http://bao:8200", "ARALDO_TRANSIT_KEY": "araldo"},
			want: "ARALDO_TRANSIT_ROLE",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"ARALDO_TRANSIT_ADDR", "ARALDO_TRANSIT_KEY", "ARALDO_TRANSIT_TOKEN", "ARALDO_TRANSIT_TOKEN_FILE", "ARALDO_TRANSIT_ROLE"} {
				t.Setenv(k, tt.env[k])
			}
			c, err := Load()
			if tt.want == "" {
				if err != nil || c.Transit.Addr == "" {
					t.Fatalf("Load = %+v, %v", c.Transit, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error %v, want one mentioning %s", err, tt.want)
			}
		})
	}
}
