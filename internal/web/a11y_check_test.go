// SPDX-License-Identifier: AGPL-3.0-or-later

package web_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// a11yIssues lists what is wrong with a page. signedIn pages have the app
// shell: a skip link and the main navigation.
func a11yIssues(page string, signedIn bool) []string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return []string{"unparseable HTML: " + err.Error()}
	}
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	ids := map[string]int{}
	labelFor := map[string]bool{}
	var all []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			all = append(all, n)
			if v := attr(n, "id"); v != "" {
				ids[v]++
			}
			if n.DataAtom == atom.Label && attr(n, "for") != "" {
				labelFor[attr(n, "for")] = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	for v, n := range ids {
		if n > 1 {
			add("id %q is used %d times", v, n)
		}
	}
	var h1, mains, navs, unnamedNavs int
	lastLevel := 0
	var first *html.Node // the first focusable element
	for _, n := range all {
		switch n.DataAtom {
		case atom.Html:
			if attr(n, "lang") == "" {
				add("<html> has no lang")
			}
		case atom.Title:
			if strings.TrimSpace(text(n)) == "" {
				add("the page has no title")
			}
		case atom.Main:
			mains++
		case atom.Nav:
			navs++
			if attr(n, "aria-label") == "" && attr(n, "aria-labelledby") == "" {
				unnamedNavs++
			}
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			level := int(n.Data[1] - '0')
			if level == 1 {
				h1++
			}
			if lastLevel > 0 && level > lastLevel+1 {
				add("heading %q skips from h%d to h%d", strings.TrimSpace(text(n)), lastLevel, level)
			}
			lastLevel = level
		case atom.Img:
			if _, ok := attrOK(n, "alt"); !ok {
				add("image %s has no alt", attr(n, "src"))
			}
		case atom.Input, atom.Select, atom.Textarea:
			switch attr(n, "type") {
			case "hidden", "submit", "button", "reset", "image":
				continue
			}
			if !named(n, labelFor) {
				add("<%s name=%q> has no label", n.Data, attr(n, "name"))
			}
			if _, ok := attrOK(n, "data-autosubmit"); ok {
				add("<%s name=%q> submits on change (WCAG 3.2.2)", n.Data, attr(n, "name"))
			}
		case atom.Button:
			if strings.TrimSpace(text(n)) == "" && attr(n, "aria-label") == "" {
				add("a button has no name")
			}
		case atom.A:
			if _, ok := attrOK(n, "href"); ok && strings.TrimSpace(text(n)) == "" && attr(n, "aria-label") == "" && !hasImgAlt(n) {
				add("a link to %s has no text", attr(n, "href"))
			}
		case atom.Table:
			if !hasDescendant(n, atom.Th) {
				add("a table has no header cells")
			}
		}
		if v, ok := attrOK(n, "tabindex"); ok {
			if i, err := strconv.Atoi(v); err == nil && i > 0 {
				add("tabindex=%d on <%s> changes the focus order", i, n.Data)
			}
		}
		for _, ref := range []string{"aria-describedby", "aria-labelledby", "aria-controls"} {
			for _, target := range strings.Fields(attr(n, ref)) {
				if ids[target] == 0 {
					add("%s points at a missing id %q", ref, target)
				}
			}
		}
		if n.DataAtom == atom.Label && attr(n, "for") != "" && ids[attr(n, "for")] == 0 {
			add("a label points at a missing id %q", attr(n, "for"))
		}
		if first == nil && focusable(n) {
			first = n
		}
	}
	if mains != 1 {
		add("%d <main> elements, want 1", mains)
	}
	if h1 != 1 {
		add("%d <h1> elements, want 1", h1)
	}
	if navs > 1 && unnamedNavs > 0 {
		add("%d navigation landmarks, %d without a name", navs, unnamedNavs)
	}
	if signedIn {
		if first == nil || first.DataAtom != atom.A || attr(first, "href") != "#main" {
			add("the first thing to focus is not a skip link to #main")
		}
		// The highlighted navigation link must say so to screen readers too.
		for _, n := range all {
			highlighted := n.DataAtom == atom.A && slices.Contains(strings.Fields(attr(n, "class")), "active")
			if highlighted != (n.DataAtom == atom.A && attr(n, "aria-current") == "page") {
				add("navigation link %q: highlighted %v, aria-current=page %v", strings.TrimSpace(text(n)), highlighted, !highlighted)
			}
		}
	}
	return issues
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func text(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && attr(n, "aria-hidden") == "true" {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// named reports whether a form control has an accessible name.
func named(n *html.Node, labelFor map[string]bool) bool {
	if attr(n, "aria-label") != "" || attr(n, "aria-labelledby") != "" || attr(n, "title") != "" || labelFor[attr(n, "id")] && attr(n, "id") != "" {
		return true
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.DataAtom == atom.Label {
			return true
		}
	}
	return false
}

func hasImgAlt(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.DataAtom == atom.Img && attr(c, "alt") != "" || hasImgAlt(c) {
			return true
		}
	}
	return false
}

func hasDescendant(n *html.Node, a atom.Atom) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.DataAtom == a || hasDescendant(c, a) {
			return true
		}
	}
	return false
}

func focusable(n *html.Node) bool {
	switch n.DataAtom {
	case atom.A:
		_, ok := attrOK(n, "href")
		return ok
	case atom.Button, atom.Select, atom.Textarea:
		return true
	case atom.Input:
		return attr(n, "type") != "hidden"
	}
	return false
}
