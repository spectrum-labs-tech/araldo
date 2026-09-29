// SPDX-License-Identifier: AGPL-3.0-or-later

// Command araldo is the single Araldo binary: it runs the API server and
// the background worker, manages the database, keys and backups, and talks
// to an Araldo API as a developer CLI. See internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spectrum-labs-tech/araldo/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
