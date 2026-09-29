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
