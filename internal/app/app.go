// SPDX-License-Identifier: AGPL-3.0-or-later

// Package app is the composition root: it opens the database and keyring,
// builds the platform registry and the use cases, and wires the HTTP
// server and the worker. Subcommands in internal/cli call it.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/ads/reddit"
	"github.com/spectrum-labs-tech/araldo/internal/analytics"
	"github.com/spectrum-labs-tech/araldo/internal/analytics/ga4"
	"github.com/spectrum-labs-tech/araldo/internal/analytics/plausible"
	"github.com/spectrum-labs-tech/araldo/internal/api"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/blob"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/config"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/email"
	"github.com/spectrum-labs-tech/araldo/internal/email/brevo"
	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/netguard"
	"github.com/spectrum-labs-tech/araldo/internal/opsched"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
	"github.com/spectrum-labs-tech/araldo/internal/platform/bluesky"
	"github.com/spectrum-labs-tech/araldo/internal/platform/discord"
	"github.com/spectrum-labs-tech/araldo/internal/platform/facebook"
	"github.com/spectrum-labs-tech/araldo/internal/platform/gab"
	"github.com/spectrum-labs-tech/araldo/internal/platform/instagram"
	"github.com/spectrum-labs-tech/araldo/internal/platform/linkedin"
	"github.com/spectrum-labs-tech/araldo/internal/platform/mastodon"
	"github.com/spectrum-labs-tech/araldo/internal/platform/pinterest"
	"github.com/spectrum-labs-tech/araldo/internal/platform/sandbox"
	"github.com/spectrum-labs-tech/araldo/internal/platform/telegram"
	"github.com/spectrum-labs-tech/araldo/internal/platform/threads"
	"github.com/spectrum-labs-tech/araldo/internal/platform/tiktok"
	"github.com/spectrum-labs-tech/araldo/internal/platform/x"
	"github.com/spectrum-labs-tech/araldo/internal/platform/youtube"
	"github.com/spectrum-labs-tech/araldo/internal/server"
	"github.com/spectrum-labs-tech/araldo/internal/store"
	"github.com/spectrum-labs-tech/araldo/internal/telemetry"
	"github.com/spectrum-labs-tech/araldo/internal/web"
)

// App is an opened Araldo.
type App struct {
	Cfg   config.Config
	Log   *slog.Logger
	Store *store.Store
	Keys  *keyring.Keyring
	Svc   *core.Service
	// DB and KeyHealth watch the database and the master keys once
	// started (Serve, RunWorker).
	DB        *store.HealthMonitor
	KeyHealth *keyring.HealthMonitor
	// meters is where instruments go: a no-op until StartTelemetry.
	meters metric.MeterProvider
	// migrate is true while a startup migration has yet to succeed.
	migrate bool
	started sync.Once
}

var registerUnavailable sync.Once

// Logger returns the JSON logger at level.
func Logger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

// Open builds the application. It degrades rather than fails: it does not
// wait for the database (requests get 503 and readiness stays false until
// Postgres answers, and a startup migration is retried until it succeeds),
// and without usable master keys only what needs a stored credential fails.
// Only configuration that cannot work as written (a malformed database URL
// or master key) is an error.
func Open(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	registerUnavailable.Do(func() { apperr.RegisterUnavailable(store.Unavailable) })
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	mk, err := masterKeys(cfg)
	if err != nil {
		st.Close()
		return nil, err
	}
	if mk == nil {
		log.WarnContext(ctx, "no master keys are configured (ARALDO_MASTER_KEYS or ARALDO_TRANSIT_ADDR): channels and other stored credentials cannot be used")
	}
	a := &App{Cfg: cfg, Log: log, Store: st, meters: noop.NewMeterProvider(), Keys: keyring.New(mk, st),
		DB: store.NewHealthMonitor(st, log, 0)}
	a.KeyHealth = keyring.NewHealthMonitor(a.Keys, log, 0)
	if cfg.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			log.WarnContext(ctx, "migration failed; retrying in the background, not ready until it succeeds", "err", err)
			a.migrate = true
		}
	}
	client := netguard.Client(cfg.AllowPrivateNetworks, 60*time.Second)
	reg := platform.NewRegistry(
		sandbox.New(cfg.BaseURL),
		bluesky.New(client),
		mastodon.New(client),
		gab.New(client),
		discord.New(client),
		telegram.New(client),
		x.New(client),
		linkedin.New(client),
		pinterest.New(client),
		youtube.New(client),
		tiktok.New(client),
		threads.New(client),
		facebook.New(client),
		instagram.New(client),
	)
	ccfg := core.Config{BaseURL: cfg.BaseURL, AllowPrivateWebhooks: cfg.AllowPrivateNetworks, MaxVideoBytes: cfg.MaxVideoBytes,
		AdNetworks: []ads.Reporter{reddit.New(client)}, AnalyticsSources: []analytics.Source{plausible.New(client), ga4.New(client)},
		Mailers: []email.Mailer{brevo.New(client)}}
	if s := cfg.S3; s.Bucket != "" {
		b, err := blob.NewS3(s.Endpoint, s.Bucket, s.Region, s.AccessKeyID, s.SecretAccessKey, s.Prefix)
		if err != nil {
			st.Close()
			return nil, err
		}
		ccfg.Blobs = b
	}
	a.Svc = core.New(st, a.Keys, reg, log, ccfg)
	return a, nil
}

// Close releases the database pool.
func (a *App) Close() { a.Store.Close() }

// start runs the health monitors and any migration still owed until ctx
// ends; Serve and RunWorker call it, and only the first call counts.
func (a *App) start(ctx context.Context) {
	a.started.Do(func() { a.startOnce(ctx) })
}

func (a *App) startOnce(ctx context.Context) {
	a.DB.Start(ctx)
	a.KeyHealth.Start(ctx)
	if !a.migrate {
		return
	}
	go func() {
		for wait := 5 * time.Second; ; wait = min(wait*2, time.Minute) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := a.Store.Migrate(ctx); err != nil {
				a.Log.WarnContext(ctx, "migration failed; retrying", "err", err, "retry_in", (wait * 2).String())
				continue
			}
			a.Log.InfoContext(ctx, "migration succeeded")
			return
		}
	}()
}

// masterKeys builds the configured master keys: the Transit key first,
// then local keys. Nil when none are configured.
func masterKeys(cfg config.Config) (*keyring.MasterKeys, error) {
	var local *keyring.MasterKeys
	if cfg.MasterKeys != "" {
		var err error
		if local, err = keyring.ParseMasterKeys(cfg.MasterKeys); err != nil {
			return nil, err
		}
	}
	if cfg.Transit.Addr == "" {
		return local, nil
	}
	t := cfg.Transit
	transit, err := keyring.NewTransit(keyring.TransitConfig{
		Addr: t.Addr, Mount: t.Mount, Key: t.Key, Token: t.Token,
		Role: t.Role, AuthPath: t.AuthPath, JWTFile: t.JWTFile,
	})
	if err != nil {
		return nil, err
	}
	if local == nil {
		return keyring.NewMasterKeys(transit)
	}
	return local.Prepend(transit)
}

// StartTelemetry starts metric export as the OTEL_* variables configure it
// (ADR 0014) and instruments the use cases. The worker started afterwards
// is instrumented too. Call stop, which flushes the exporter, when done.
func (a *App) StartTelemetry(ctx context.Context) (stop func(), err error) {
	t, err := telemetry.Start(ctx, a.Log, "araldo", buildinfo.Version)
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	stop = func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := t.Shutdown(ctx); err != nil {
			a.Log.WarnContext(ctx, "telemetry shutdown failed", "err", err)
		}
	}
	if err := a.Svc.Instrument(t.MeterProvider()); err != nil {
		stop()
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	a.meters = t.MeterProvider()
	return stop, nil
}

// Handler is the HTTP server: API, dashboard, health checks.
func (a *App) Handler() (http.Handler, error) {
	dash, err := web.New(a.Svc, a.Log, web.Config{SecureCookies: !a.Cfg.InsecureCookies, ClientIPHeader: a.Cfg.ClientIPHeader})
	if err != nil {
		return nil, err
	}
	return server.Handler(api.New(a.Svc, a.Log), dash, a.Svc.Ready, a.DB, a.Log), nil
}

// Serve runs the HTTP server until ctx ends.
func (a *App) Serve(ctx context.Context) error {
	a.start(ctx)
	h, err := a.Handler()
	if err != nil {
		return err
	}
	return server.Serve(ctx, a.Cfg.Listen, h, a.Log)
}

// RunWorker publishes, delivers webhooks and runs periodic tasks until ctx
// ends.
func (a *App) RunWorker(ctx context.Context) error {
	a.start(ctx)
	host, _ := os.Hostname()
	owner := host + "/" + strconv.Itoa(os.Getpid())
	sched, err := opsched.New(a.Store, a.Log, a.Svc.Tasks()...)
	if err != nil {
		return err
	}
	sched.Owner = owner
	if err := sched.Instrument(a.meters); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	run := func(name string, fn func(context.Context) error) {
		wg.Go(func() {
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
		})
	}
	a.Log.InfoContext(ctx, "worker started", "owner", owner)
	run("publisher", func(ctx context.Context) error { return a.Svc.RunPublisher(ctx, owner) })
	run("webhooks", func(ctx context.Context) error { return a.Svc.RunDeliverer(ctx, owner) })
	run("tasks", sched.Run)
	wg.Wait()
	close(errs)
	var msgs []string
	for err := range errs {
		msgs = append(msgs, err.Error())
	}
	if len(msgs) > 0 {
		return fmt.Errorf("worker: %s", strings.Join(msgs, "; "))
	}
	return nil
}
