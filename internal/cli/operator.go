// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// runAdminOperatorKeys manages the keys of the operator API (ADR 0031),
// which the install's own service and these admin commands use (ADR 0035).
func runAdminOperatorKeys(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr("operator-keys create --name NAME | list | revoke --key ID")
	}
	cmd := args[0]
	var of operatorFlags
	var name, keyRef string
	switch cmd {
	case "create":
		if err := flags("operator-keys create", stderr, args[1:], func(fs *flag.FlagSet) {
			of.define(fs)
			fs.StringVar(&name, "name", "", "what uses the key (required)")
		}); err != nil {
			return err
		}
		if name == "" {
			return usageErr("--name is required")
		}
	case "list":
		if err := flags("operator-keys list", stderr, args[1:], of.define); err != nil {
			return err
		}
	case "revoke":
		if err := flags("operator-keys revoke", stderr, args[1:], func(fs *flag.FlagSet) {
			of.define(fs)
			fs.StringVar(&keyRef, "key", "", "the key's ID, from operator-keys list (required)")
		}); err != nil {
			return err
		}
		if keyRef == "" {
			return usageErr("--key is required")
		}
		if _, err := id.Parse(id.OperatorKey, keyRef); err != nil {
			return usageErr("--key is a key ID such as opkey_…, from araldo admin operator-keys list")
		}
	default:
		return usageErr("unknown operator-keys command %q", cmd)
	}
	c, err := operatorClient(of.hostname)
	if err != nil {
		return err
	}
	type operatorKey struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Hint       string     `json:"hint"`
		Secret     string     `json:"secret"`
		LastUsedAt *time.Time `json:"last_used_at"`
	}
	switch cmd {
	case "create":
		var k operatorKey
		if err := call(ctx, c, http.MethodPost, "/v1/operator/operator_keys", nil, map[string]string{"name": name}, &k); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Created %s (%s). It is shown once: store it where your service reads it.\n", k.Name, k.ID)
		_, _ = fmt.Fprintln(stdout, k.Secret)
		return nil
	case "list":
		var keys listOf[operatorKey]
		if err := call(ctx, c, http.MethodGet, "/v1/operator/operator_keys", nil, nil, &keys); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tKEY\tLAST USED")
		for _, k := range keys.Data {
			used := "never"
			if k.LastUsedAt != nil {
				used = k.LastUsedAt.UTC().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Hint, used)
		}
		return tw.Flush()
	}
	if err := call(ctx, c, http.MethodDelete, "/v1/operator/operator_keys/"+keyRef, nil, nil, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stderr, "Revoked. It stopped working at once.")
	return nil
}

// parseLimits reads --limits: "brands=3,channels=10,members=5,
// posts_per_month=200", each "none" for no limit; any left out is none.
func parseLimits(s string) (model.OrgLimits, error) {
	var l model.OrgLimits
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return l, usageErr("--limits takes name=number pairs, such as brands=3,posts_per_month=200")
		}
		var n *int
		if v != "none" {
			i, err := strconv.Atoi(v)
			if err != nil || i < 0 {
				return l, usageErr("--limits: %s is a number from 0 up, or none", k)
			}
			n = &i
		}
		switch k {
		case "brands":
			l.Brands = n
		case "channels":
			l.Channels = n
		case "members":
			l.Members = n
		case "posts_per_month":
			l.PostsMonth = n
		default:
			return l, usageErr("--limits: %q is not a limit; they are brands, channels, members and posts_per_month", k)
		}
	}
	return l, nil
}
