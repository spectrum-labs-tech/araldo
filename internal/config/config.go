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

	"github.com/spectrum-labs-tech/araldo/internal/netguard"
)

// Config is every setting.
type Config struct {
	// DatabaseURL is a Postgres URL (ARALDO_DATABASE_URL, or DATABASE_URL).
	DatabaseURL string
	// MasterKeys are local master keys, "id:base64key,..."
	// (ARALDO_MASTER_KEYS), or read from ARALDO_MASTER_KEYS_FILE (ADR 0008).
	MasterKeys string
	// Transit is a master key held by a Transit engine (OpenBao or Vault).
	// When set it is the primary master key, and local keys only unwrap
	// what they wrapped before.
	Transit Transit
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
	// PrivateNetworks are the non-public addresses platform adapters may
	// reach, such as a self-hosted Mastodon (ARALDO_ALLOW_PRIVATE_NETWORKS:
	// true, or a list of networks).
	PrivateNetworks netguard.Policy
	// PrivateWebhooks are the non-public addresses webhook deliveries and
	// media fetched by URL may reach (ARALDO_ALLOW_PRIVATE_WEBHOOKS). They
	// are kept apart because every org chooses those URLs.
	PrivateWebhooks netguard.Policy
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
	// SignupURL is where people create an account and org, on an install
	// whose orgs come from the operator (ARALDO_SIGNUP_URL, ADR 0031).
	SignupURL string
	// BillingURL is where owners manage billing (ARALDO_BILLING_URL),
	// reached with a hand-off signed with BillingLinkKey
	// (ARALDO_BILLING_LINK_KEY, or read from ARALDO_BILLING_LINK_KEY_FILE;
	// at least 32 characters).
	BillingURL     string
	BillingLinkKey string
	// SMTP sends the install's own email (ADR 0034); none without a host.
	SMTP SMTP
}

// SMTP is the server the install's email goes through (ARALDO_SMTP_*).
type SMTP struct {
	Host     string // ARALDO_SMTP_HOST; empty sends no email
	Port     int    // ARALDO_SMTP_PORT (default 587, or 465 with TLS tls)
	Username string // ARALDO_SMTP_USERNAME
	Password string // ARALDO_SMTP_PASSWORD, or read from ARALDO_SMTP_PASSWORD_FILE
	From     string // ARALDO_SMTP_FROM, as "Araldo <noreply@example.com>"
	TLS      string // ARALDO_SMTP_TLS: starttls (default), tls, or none for a local relay
}

// MinBillingLinkKey is the shortest billing link key accepted.
const MinBillingLinkKey = 32

// Transit reaches a Transit key (ARALDO_TRANSIT_*). Addr and Key turn it
// on; it authenticates with Token, or logs in as Role with the pod's
// ServiceAccount token.
type Transit struct {
	Addr     string // ARALDO_TRANSIT_ADDR, e.g. http://openbao:8200
	Mount    string // ARALDO_TRANSIT_MOUNT (default transit)
	Key      string // ARALDO_TRANSIT_KEY, the key's name
	Token    string // ARALDO_TRANSIT_TOKEN, or read from ARALDO_TRANSIT_TOKEN_FILE
	Role     string // ARALDO_TRANSIT_ROLE, for Kubernetes (or JWT) auth
	AuthPath string // ARALDO_TRANSIT_AUTH_PATH (default kubernetes)
	JWTFile  string // ARALDO_TRANSIT_JWT_FILE (default the ServiceAccount token)
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

// Load reads the environment. Master keys are optional: without them
// Araldo runs, but cannot use stored credentials (see app.Open).
func Load() (Config, error) {
	c := Config{
		DatabaseURL: first(os.Getenv("ARALDO_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		MasterKeys:  os.Getenv("ARALDO_MASTER_KEYS"),
		Transit: Transit{
			Addr:     os.Getenv("ARALDO_TRANSIT_ADDR"),
			Mount:    os.Getenv("ARALDO_TRANSIT_MOUNT"),
			Key:      os.Getenv("ARALDO_TRANSIT_KEY"),
			Token:    os.Getenv("ARALDO_TRANSIT_TOKEN"),
			Role:     os.Getenv("ARALDO_TRANSIT_ROLE"),
			AuthPath: os.Getenv("ARALDO_TRANSIT_AUTH_PATH"),
			JWTFile:  os.Getenv("ARALDO_TRANSIT_JWT_FILE"),
		},
		BaseURL:        strings.TrimRight(first(os.Getenv("ARALDO_BASE_URL"), "http://localhost:8080"), "/"),
		SignupURL:      os.Getenv("ARALDO_SIGNUP_URL"),
		BillingURL:     os.Getenv("ARALDO_BILLING_URL"),
		BillingLinkKey: os.Getenv("ARALDO_BILLING_LINK_KEY"),
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
		SMTP: SMTP{
			Host:     os.Getenv("ARALDO_SMTP_HOST"),
			Username: os.Getenv("ARALDO_SMTP_USERNAME"),
			Password: os.Getenv("ARALDO_SMTP_PASSWORD"),
			From:     os.Getenv("ARALDO_SMTP_FROM"),
			TLS:      strings.ToLower(os.Getenv("ARALDO_SMTP_TLS")),
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
	if c.PrivateNetworks, err = netguard.ParsePolicy(os.Getenv("ARALDO_ALLOW_PRIVATE_NETWORKS")); err != nil {
		errs = append(errs, fmt.Errorf("ARALDO_ALLOW_PRIVATE_NETWORKS: %w", err))
	}
	if c.PrivateWebhooks, err = netguard.ParsePolicy(os.Getenv("ARALDO_ALLOW_PRIVATE_WEBHOOKS")); err != nil {
		errs = append(errs, fmt.Errorf("ARALDO_ALLOW_PRIVATE_WEBHOOKS: %w", err))
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
	if f := os.Getenv("ARALDO_TRANSIT_TOKEN_FILE"); f != "" && c.Transit.Token == "" {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the operator chooses this path
		if err != nil {
			errs = append(errs, fmt.Errorf("ARALDO_TRANSIT_TOKEN_FILE: %w", err))
		}
		c.Transit.Token = strings.TrimSpace(string(b))
	}
	if v := os.Getenv("ARALDO_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("ARALDO_SMTP_PORT %q is not a port", v))
		} else {
			c.SMTP.Port = n
		}
	}
	if f := os.Getenv("ARALDO_SMTP_PASSWORD_FILE"); f != "" && c.SMTP.Password == "" {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the operator chooses this path
		if err != nil {
			errs = append(errs, fmt.Errorf("ARALDO_SMTP_PASSWORD_FILE: %w", err))
		}
		c.SMTP.Password = strings.TrimSpace(string(b))
	}
	if s := c.SMTP; s.Host != "" && s.From == "" {
		errs = append(errs, errors.New("ARALDO_SMTP_HOST needs ARALDO_SMTP_FROM, the address mail comes from"))
	}
	if f := os.Getenv("ARALDO_BILLING_LINK_KEY_FILE"); f != "" && c.BillingLinkKey == "" {
		b, err := os.ReadFile(f) //nolint:gosec // G304: the operator chooses this path
		if err != nil {
			errs = append(errs, fmt.Errorf("ARALDO_BILLING_LINK_KEY_FILE: %w", err))
		}
		c.BillingLinkKey = strings.TrimSpace(string(b))
	}
	for name, v := range map[string]string{"ARALDO_SIGNUP_URL": c.SignupURL, "ARALDO_BILLING_URL": c.BillingURL} {
		if u, err := url.Parse(v); v != "" && (err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "") {
			errs = append(errs, fmt.Errorf("%s %q is not an absolute URL", name, v))
		}
	}
	if c.BillingURL != "" && len(c.BillingLinkKey) < MinBillingLinkKey {
		errs = append(errs, fmt.Errorf("ARALDO_BILLING_URL needs ARALDO_BILLING_LINK_KEY (or _FILE) of at least %d characters", MinBillingLinkKey))
	}
	if t := c.Transit; t.Addr != "" || t.Key != "" {
		if t.Addr == "" || t.Key == "" {
			errs = append(errs, errors.New("ARALDO_TRANSIT_ADDR and ARALDO_TRANSIT_KEY go together"))
		}
		if t.Token == "" && t.Role == "" {
			errs = append(errs, errors.New("Transit needs ARALDO_TRANSIT_TOKEN (or _FILE) or ARALDO_TRANSIT_ROLE"))
		}
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("ARALDO_DATABASE_URL is required"))
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
