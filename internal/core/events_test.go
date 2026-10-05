// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestEmittedEventsAreListed reads this package's source and checks that
// every event type given to emit as a literal is in EventTypes, the list
// endpoints subscribe from, so a new event cannot be emitted yet
// unsubscribable. Types computed at the call ("post." + status) are checked
// by emit itself when a test reaches them; TestEventTypesMatchContract keeps
// EventTypes and the contract's EventType enum equal.
func TestEmittedEventsAreListed(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	literals := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "emit" || len(call.Args) != 7 {
				return true
			}
			lit, ok := call.Args[5].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			typ, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			literals++
			if !slices.Contains(EventTypes, typ) {
				t.Errorf("%s: emits %q, which is not in EventTypes (nor, then, subscribable)", fset.Position(lit.Pos()), typ)
			}
			return true
		})
	}
	if literals == 0 {
		t.Fatal("found no emit calls: has emit's signature changed?")
	}
}

// TestEmitRefusesAnUnlistedEvent checks the guard in emit itself, for
// types the source scan cannot see.
func TestEmitRefusesAnUnlistedEvent(t *testing.T) {
	t.Parallel()
	err := (&Service{}).emit(t.Context(), nil, [16]byte{}, false, "", "post.vanished", nil)
	if err == nil || !strings.Contains(err.Error(), "not in core.EventTypes") {
		t.Fatalf("emit of an unlisted type: %v", err)
	}
}
