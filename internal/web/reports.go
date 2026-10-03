// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Reports (ADR 0026).

type reportData struct {
	Brands []*model.Brand
	Brand  *model.Brand
	// Month is the period as the month picker shows it, when it is one.
	Month  string
	Report *core.Report
}

func (s *Server) reportsPage(c *reqCtx) error {
	q := c.r.URL.Query()
	d := &reportData{}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	if len(d.Brands) == 0 {
		return s.page(c, "reports", "reports", "Reports", d)
	}
	d.Brand = d.Brands[0]
	if ref := q.Get("brand"); ref != "" {
		if d.Brand, err = s.svc.ResolveBrand(c.ctx(), c.actor, ref); err != nil {
			return err
		}
	}
	in := core.ReportInput{BrandID: d.Brand.ID, Month: q.Get("month")}
	if d.Report, err = s.svc.BrandReport(c.ctx(), c.actor, in); err != nil {
		in.Month = ""
		var derr error
		if d.Report, derr = s.svc.BrandReport(c.ctx(), c.actor, in); derr != nil {
			return derr
		}
		d.Month = q.Get("month")
		return s.formErr(c, "reports", "reports", "Report: "+d.Brand.Name, d, err)
	}
	if r := d.Report; r.Since.Day() == 1 && r.Until.AddDate(0, 0, 1).Day() == 1 && r.Since.Month() == r.Until.Month() {
		d.Month = r.Since.Format("2006-01")
	}
	return s.page(c, "reports", "reports", "Report: "+d.Brand.Name, d)
}

// change describes how a figure moved from the period before: "+12%",
// "−3%", "new" or "no change".
func change(p core.Pair) string {
	switch {
	case p.Now == p.Before:
		return "no change"
	case p.Before == 0:
		return "new"
	}
	pct := float64(p.Now-p.Before) * 100 / float64(p.Before)
	sign := "+"
	if pct < 0 {
		sign, pct = "−", -pct
	}
	if pct < 10 {
		return sign + strconv.FormatFloat(pct, 'f', 1, 64) + "%"
	}
	return fmt.Sprintf("%s%.0f%%", sign, pct)
}

// changeClass colors a change: good when it moved the way that is better,
// bad the other way. lowerBetter is "true" for failures, costs and
// unsubscribes.
func changeClass(p core.Pair, lowerBetter string) string {
	switch {
	case p.Now == p.Before:
		return "neutral"
	case (p.Now > p.Before) == (lowerBetter != "true"):
		return "good"
	}
	return "bad"
}

// statCard is a figure for the report's stat template.
type statCard struct {
	Label       string
	Pair        core.Pair
	Currency    string
	LowerBetter string
}

// fmtPeriod names a period: "September 2026", or "Sep 10 – Sep 16, 2026".
func fmtPeriod(since, until time.Time) string {
	switch {
	case since.Day() == 1 && until.AddDate(0, 0, 1).Day() == 1 && since.Month() == until.Month() && since.Year() == until.Year():
		return since.Format("January 2006")
	case since.Year() == until.Year():
		return since.Format("Jan 2") + " – " + until.Format("Jan 2, 2006")
	}
	return since.Format("Jan 2, 2006") + " – " + until.Format("Jan 2, 2006")
}
