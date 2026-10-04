// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"io"
	"net/url"
	"os"

	"github.com/spectrum-labs-tech/araldo/internal/mcp"
)

// runMCP serves Araldo's tools to an AI assistant over stdio (ADR 0020).
// It needs no database: it calls an Araldo API with a key.
func runMCP(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	base, key := os.Getenv("ARALDO_URL"), os.Getenv("ARALDO_API_KEY")
	if err := flags("mcp", stderr, args, func(fs *flag.FlagSet) {
		fs.StringVar(&base, "url", base, "the Araldo to use, e.g. https://araldo.example.com (or ARALDO_URL)")
	}); err != nil {
		return err
	}
	if base == "" || key == "" {
		// Otherwise the server and credential `araldo auth login` stored.
		c, _, err := connect(base)
		if err != nil {
			return usageErr("sign in with araldo auth login, or set ARALDO_URL (or --url) and ARALDO_API_KEY; the credential decides what the tools may do (%v)", err)
		}
		base, key = c.BaseURL, c.Token
	}
	if u, err := url.Parse(base); err != nil || u.Scheme == "" || u.Host == "" {
		return usageErr("ARALDO_URL %q is not an absolute URL", base)
	}
	return mcp.NewServer(mcp.NewClient(base, key)).Serve(ctx, stdin, stdout)
}
