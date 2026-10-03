// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config reads Araldo's settings from ARALDO_* environment
// variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is every setting.
type Config struct {
	// DatabaseURL is a Postgres URL (ARALDO_DATABASE_URL, or DATABASE_URL).
	DatabaseURL string
	// MasterKeys are "id:base64key,..." (ARALDO_MASTER_KEYS), or read from
	// ARALDO_MASTER_KEYS_FILE (ADR 0008).
	MasterKeys string
	// BaseURL is the public URL (ARALDO_BASE_URL).
	BaseURL string
	// Listen is the HTTP address (ARALDO_LISTEN, default :8080).
	Listen string
	// AutoMigrate runs migrations at startup (ARALDO_AUTO_MIGRATE, default
	// true).
	AutoMigrate bool
	// InsecureCookies drops the Secure flag, for plain-HTTP development
	// (ARALDO_INSECURE_COOKIES).
	InsecureCookies bool
	// AllowPrivateNetworks lets webhooks and platform adapters reach
	// private addresses (ARALDO_ALLOW_PRIVATE_NETWORKS).
	AllowPrivateNetworks bool
	// ClientIPHeader is a trusted proxy header with the client IP
	// (ARALDO_CLIENT_IP_HEADER, for example CF-Connecting-IP).
	ClientIPHeader string
	// LogLevel is debug, info, warn or error (ARALDO_LOG_LEVEL).
	LogLevel string
	// S3 stores new media files in S3-compatible storage when its Bucket
	// is set; otherwise they go in Postgres (ADR 0017).
	S3 S3
	// MaxVideoBytes is the largest video accepted (ARALDO_MAX_VIDEO_BYTES,
	// default 1 GiB); video needs S3 (ADR 0027).
	MaxVideoBytes int64
}

// S3 is an S3-compatible bucket (ARALDO_S3_*).
type S3 struct {
	Endpoint        string // ARALDO_S3_ENDPOINT, e.g. https://<account>.r2.cloudflarestorage.com
	Bucket          string // ARALDO_S3_BUCKET
	Region          string // ARALDO_S3_REGION (default auto)
	AccessKeyID     string // ARALDO_S3_ACCESS_KEY_ID
	SecretAccessKey string // ARALDO_S3_SECRET_ACCESS_KEY
	Prefix          string // ARALDO_S3_PREFIX (default media/)
}

// Load reads the environment. needKeys requires master keys (server and
// worker); one-off commands that touch no secrets can skip them.
func Load(needKeys bool) (Config, error) {
	c := Config{
		DatabaseURL:    first(os.Getenv("ARALDO_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		MasterKeys:     os.Getenv("ARALDO_MASTER_KEYS"),
		BaseURL:        strings.TrimRight(first(os.Getenv("ARALDO_BASE_URL"), "http://localhost:8080"), "/"),
		Listen:         first(os.Getenv("ARALDO_LISTEN"), ":8080"),
		ClientIPHeader: os.Getenv("ARALDO_CLIENT_IP_HEADER"),
		LogLevel:       first(os.Getenv("ARALDO_LOG_LEVEL"), "info"),
		AutoMigrate:    true,
		S3: S3{
			Endpoint:        os.Getenv("ARALDO_S3_ENDPOINT"),
			Bucket:          os.Getenv("ARALDO_S3_BUCKET"),
			Region:          first(os.Getenv("ARALDO_S3_REGION"), "auto"),
			AccessKeyID:     os.Getenv("ARALDO_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("ARALDO_S3_SECRET_ACCESS_KEY"),
			Prefix:          first(os.Getenv("ARALDO_S3_PREFIX"), "media/"),
		},
	}
	var errs []error
	var err error
	if c.AutoMigrate, err = boolEnv("ARALDO_AUTO_MIGRATE", true); err != nil {
		errs = append(errs, err)
	}
	if c.InsecureCookies, err = boolEnv("ARALDO_INSECURE_COOKIES", false); err != nil {
		errs = append(errs, err)
	}
	if c.AllowPrivateNetworks, err = boolEnv("ARALDO_ALLOW_PRIVATE_NETWORKS", false); err != nil {
		errs = append(errs, err)
	}
	c.MaxVideoBytes = 1 << 30
	if v := os.Getenv("ARALDO_MAX_VIDEO_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("ARALDO_MAX_VIDEO_BYTES %q is not a positive number of bytes", v))
		} else {
			c.MaxVideoBytes = n
		}
	}
	if f := os.Getenv("ARALDO_MASTER_KEYS_FILE"); f != "" && c.MasterKeys == "" {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the operator chooses this path
		if err != nil {
			errs = append(errs, fmt.Errorf("ARALDO_MASTER_KEYS_FILE: %w", err))
		}
		c.MasterKeys = strings.TrimSpace(string(b))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("ARALDO_DATABASE_URL is required"))
	}
	if needKeys && c.MasterKeys == "" {
		errs = append(errs, errors.New("ARALDO_MASTER_KEYS is required (generate one with: araldo keys generate)"))
	}
	if u, err := url.Parse(c.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("ARALDO_BASE_URL %q is not an absolute URL", c.BaseURL))
	}
	if s := c.S3; s.Bucket != "" && (s.Endpoint == "" || s.AccessKeyID == "" || s.SecretAccessKey == "") {
		errs = append(errs, errors.New("ARALDO_S3_BUCKET needs ARALDO_S3_ENDPOINT, ARALDO_S3_ACCESS_KEY_ID and ARALDO_S3_SECRET_ACCESS_KEY"))
	}
	return c, errors.Join(errs...)
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolEnv(name string, def bool) (bool, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s must be true or false, not %q", name, v)
	}
	return b, nil
}
