// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Reports (ADR 0026).

type reportData struct {
	Brands []*model.Brand
	Brand  *model.Brand
	// Month is the period as the month picker shows it, when it is one.
	Month  string
	Report *core.Report
	// CanShare is set for those who make share links, Shares are the
	// brand's open ones, and ShareLink the one just made, shown once.
	CanShare  bool
	Shares    []*model.ReportShare
	ShareLink string
	// Recipients get the brand's report by email each month (ADR 0026),
	// for those who share.
	Recipients  []model.ReportRecipient
	MailEnabled bool
}

// reportView is the report partial's data.
type reportView struct {
	R      *core.Report
	Public bool
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
	if d.CanShare = c.actor.Can(core.PermMembersWrite); d.CanShare {
		if d.Shares, err = s.svc.ReportShares(c.ctx(), c.actor, d.Brand.ID); err != nil {
			return err
		}
		if d.Recipients, err = s.svc.ReportRecipients(c.ctx(), c.actor, d.Brand.ID); err != nil {
			return err
		}
	}
	d.MailEnabled = s.svc.MailEnabled()
	return s.page(c, "reports", "reports", "Report: "+d.Brand.Name, d)
}

// reportRecipients adds or removes an address the brand's monthly report
// goes to.
func (s *Server) reportRecipients(c *reqCtx) error {
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, c.r.PostFormValue("brand"))
	if err != nil {
		return err
	}
	email, notice := c.r.PostFormValue("email"), ""
	back := "/reports?" + url.Values{"brand": {id.Format(id.Brand, b.ID)}}.Encode()
	if c.r.PostFormValue("action") == "remove" {
		err, notice = s.svc.RemoveReportRecipient(c.ctx(), c.actor, b.ID, email), email+" no longer gets the monthly report."
	} else {
		err, notice = s.svc.AddReportRecipient(c.ctx(), c.actor, b.ID, email), email+" gets the report early each month."
	}
	if err != nil {
		if ae := apperr.As(err); ae.Kind == apperr.KindInvalid || ae.Kind == apperr.KindConflict {
			return redirect(c, back, ae.Message)
		}
		return err
	}
	return redirect(c, back, notice)
}

// shareReport makes a link to the brand's month and shows it once, on the
// report it shares.
func (s *Server) shareReport(c *reqCtx) error {
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, c.r.PostFormValue("brand"))
	if err != nil {
		return err
	}
	month := c.r.PostFormValue("month")
	token, _, err := s.svc.CreateReportShare(c.ctx(), c.actor, b.ID, month)
	if err != nil {
		return err
	}
	c.r.URL.RawQuery = url.Values{"brand": {id.Format(id.Brand, b.ID)}, "month": {month}}.Encode()
	d := &reportData{Brand: b, Month: month, CanShare: true, ShareLink: s.svc.ShareURL(token)}
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	if d.Report, err = s.svc.BrandReport(c.ctx(), c.actor, core.ReportInput{BrandID: b.ID, Month: month}); err != nil {
		return err
	}
	if d.Shares, err = s.svc.ReportShares(c.ctx(), c.actor, b.ID); err != nil {
		return err
	}
	return s.page(c, "reports", "reports", "Report: "+b.Name, d)
}

func (s *Server) unshareReport(c *reqCtx) error {
	shareID, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		return apperr.NotFound("shared report")
	}
	if err := s.svc.RevokeReportShare(c.ctx(), c.actor, shareID); err != nil {
		return err
	}
	return redirect(c, "/reports?"+url.Values{"brand": {c.r.PostFormValue("brand")}, "month": {c.r.PostFormValue("month")}}.Encode(),
		"The link no longer works.")
}

// sharedReport shows a shared report to anyone holding its link, with no
// sign-in: read-only, nothing linking into the dashboard, not indexed.
func (s *Server) sharedReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "no-referrer")
	rep, _, err := s.svc.SharedReport(r.Context(), r.PathValue("token"))
	if err != nil {
		v := s.view(nil, "", "Report not found", nil)
		v.Public = true
		status := http.StatusNotFound
		if apperr.As(err).Kind != apperr.KindNotFound {
			status = http.StatusServiceUnavailable
			s.log.ErrorContext(r.Context(), "showing a shared report", "err", err)
		}
		s.render(w, status, "report_shared", v)
		return
	}
	v := s.view(nil, "", rep.Brand.Name+": report", reportView{R: rep, Public: true})
	v.Public = true
	s.render(w, http.StatusOK, "report_shared", v)
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
