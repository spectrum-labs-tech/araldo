// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
	"github.com/spectrum-labs-tech/araldo/internal/buildinfo"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/hosts"
)

// araldo listen (ADR 0012, like stripe listen): the org's events as they
// happen, printed, and forwarded signed to a local URL, so webhooks can be
// built against a laptop with no public address.

// listenRetry is how long listen waits before reconnecting.
var listenRetry = 3 * time.Second

func newListenSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b)
}

func runListen(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var t target
	var forwardTo, events string
	var printJSON bool
	if err := flags("listen", stderr, args, func(fs *flag.FlagSet) {
		clientFlags(fs, &t)
		fs.StringVar(&forwardTo, "forward-to", "", "a URL to POST each event to, signed like a webhook, e.g. http://localhost:3000/webhooks")
		fs.StringVar(&events, "events", "", "only these event types, comma-separated (default all)")
		fs.BoolVar(&printJSON, "print-json", false, "print each event as JSON")
	}); err != nil {
		return err
	}
	if forwardTo != "" {
		if u, err := url.Parse(forwardTo); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return usageErr("--forward-to %q is not an http or https URL", forwardTo)
		}
	}
	c, cred, err := connect(t)
	if err != nil {
		return err
	}
	store, err := openHosts()
	if err != nil {
		return err
	}
	secret, err := store.ListenSecret(cred.Name, newListenSecret)
	if err != nil {
		return err
	}
	q := url.Values{}
	if events != "" {
		q.Set("types", strings.ReplaceAll(events, " ", ""))
	}
	_, _ = fmt.Fprintf(stderr, "> Ready! Listening to %s's events in %s mode. Your webhook signing secret is %s (^C to quit)\n",
		cred.Name, hosts.Mode(t.live), secret)
	if forwardTo != "" {
		_, _ = fmt.Fprintf(stderr, "> Forwarding to %s\n", forwardTo)
	}
	forward := &http.Client{Timeout: 30 * time.Second}
	var last string
	for {
		err := c.Stream(ctx, "/v1/events/stream", q, last, func(e apiclient.Event) error {
			last = e.ID
			if printJSON {
				_, _ = fmt.Fprintln(stdout, string(e.Data))
			} else {
				_, _ = fmt.Fprintf(stdout, "%s   --> %s [%s]\n", time.Now().Format(time.DateTime), e.Type, e.ID)
			}
			if forwardTo != "" {
				deliver(ctx, forward, forwardTo, secret, e, stdout)
			}
			return nil
		})
		select {
		case <-ctx.Done():
			return nil // ^C
		default:
		}
		var ae *apiclient.APIError
		if errors.As(err, &ae) && ae.Status/100 == 4 {
			return err // a refusal: reconnecting would be refused again
		}
		reason := "the stream ended"
		if err != nil {
			reason = err.Error()
		}
		_, _ = fmt.Fprintf(stderr, "! Lost the connection (%s); reconnecting in %s\n", reason, listenRetry)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(listenRetry):
		}
	}
}

// deliver POSTs one event to url, signed as Araldo signs webhooks, and
// prints the answer.
func deliver(ctx context.Context, client *http.Client, url, secret string, e apiclient.Event, stdout io.Writer) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(e.Data))
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "%s  <--  [ERR] %v\n", time.Now().Format(time.DateTime), err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "araldo-cli/"+buildinfo.Version)
	req.Header.Set("Araldo-Signature", core.Sign(secret, time.Now(), e.Data))
	req.Header.Set("Araldo-Event-Id", e.ID)
	resp, err := client.Do(req) //nolint:gosec // G704: the URL the developer named, on their machine
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "%s  <--  [ERR] POST %s: %v\n", time.Now().Format(time.DateTime), url, err)
		return
	}
	_ = resp.Body.Close()
	_, _ = fmt.Fprintf(stdout, "%s  <--  [%d] POST %s [%s]\n", time.Now().Format(time.DateTime), resp.StatusCode, url, e.ID)
}
