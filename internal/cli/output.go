// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/itchyny/gojq"
)

// output prints what a client command fetched the way gh does (ADR 0028):
// an aligned table with a header in a terminal, tab-separated rows when
// piped, or JSON with --json fields, filtered by --jq.
type output struct {
	w   io.Writer
	tty bool
	// fields are the --json fields; nil prints a table.
	fields []string
	jq     string
}

// isTerminal reports whether w is a terminal (a character device).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// bareJSON reports whether --json was given without fields (last, or
// followed by another flag). gh answers that with the fields it offers.
func bareJSON(args []string) bool {
	for i, a := range args {
		if a == "--json" || a == "-json" {
			return i == len(args)-1 || strings.HasPrefix(args[i+1], "-")
		}
	}
	return false
}

// jsonFieldsHelp is gh's answer to a bare --json.
func jsonFieldsHelp(fields []string) error {
	return usageErr("specify one or more comma-separated fields for --json:\n  %s", strings.Join(fields, "\n  "))
}

// empty says, in a terminal, that a list has nothing in it, as gh does;
// piped, an empty list prints nothing.
func (o *output) empty(stderr io.Writer, what string) {
	if o.tty {
		_, _ = fmt.Fprintln(stderr, what)
	}
}

// newOutput checks --json and --jq against the fields a command offers.
func newOutput(w io.Writer, jsonFields, jq string, available []string) (*output, error) {
	o := &output{w: w, tty: isTerminal(w), jq: jq}
	if jsonFields == "" {
		if jq != "" {
			return nil, usageErr("--jq needs --json")
		}
		return o, nil
	}
	for f := range strings.SplitSeq(jsonFields, ",") {
		f = strings.TrimSpace(f)
		if !contains(available, f) {
			return nil, usageErr("unknown JSON field %q; available fields:\n  %s", f, strings.Join(available, "\n  "))
		}
		o.fields = append(o.fields, f)
	}
	return o, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// printJSON prints items, keeping only the --json fields, through --jq if
// given.
func (o *output) printJSON(items []map[string]any) error {
	picked := make([]any, len(items))
	for i, it := range items {
		m := map[string]any{}
		for _, f := range o.fields {
			m[f] = it[f]
		}
		picked[i] = m
	}
	if o.jq == "" {
		enc := json.NewEncoder(o.w)
		enc.SetIndent("", "  ")
		return enc.Encode(picked)
	}
	return runJQ(o.w, o.jq, picked)
}

// runJQ prints each result of a jq query: strings bare, as jq -r does,
// anything else as JSON.
func runJQ(w io.Writer, expr string, input any) error {
	q, err := gojq.Parse(expr)
	if err != nil {
		return usageErr("--jq: %v", err)
	}
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, isErr := v.(error); isErr {
			var halt *gojq.HaltError
			if errors.As(err, &halt) && halt.Value() == nil {
				return nil
			}
			return fmt.Errorf("--jq: %w", err)
		}
		if s, isString := v.(string); isString {
			_, _ = fmt.Fprintln(w, s)
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, string(b))
	}
}

// table prints rows aligned under a header in a terminal, or as
// tab-separated values without one when piped, for cut and awk.
func (o *output) table(header []string, rows [][]string) error {
	if !o.tty {
		for _, r := range rows {
			if _, err := fmt.Fprintln(o.w, strings.Join(r, "\t")); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(o.w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// str reads a string field of a decoded JSON object.
func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
