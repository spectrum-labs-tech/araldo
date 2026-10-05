// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/authn"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Report shares (ADR 0026): a link to one brand's report for one month, for
// a client or anyone else outside the org, readable without an account.

// ShareURL is a share link's address.
func (s *Service) ShareURL(token string) string { return s.cfg.BaseURL + "/r/" + token }

// ReportShareTTL is how long a share link works.
const ReportShareTTL = 30 * 24 * time.Hour

var errShareInvalid = apperr.NotFound("shared report")

// CreateReportShare makes a link to a brand's report for month (YYYY-MM)
// and returns its token, shown once. Admins and owners share.
func (s *Service) CreateReportShare(ctx context.Context, a Actor, brandID uuid.UUID, month string) (string, *model.ReportShare, error) {
	if err := a.require(PermMembersWrite); err != nil {
		return "", nil, err
	}
	b, err := s.Brand(ctx, a, brandID)
	if err != nil {
		return "", nil, err
	}
	if _, _, err := reportPeriod(ReportInput{Month: month}, b, s.Now()); err != nil || month == "" {
		if err == nil {
			err = apperr.Invalid("month_invalid", "month", "Share a month, as YYYY-MM.")
		}
		return "", nil, err
	}
	token, hash := authn.NewToken()
	sh := &model.ReportShare{ID: id.New(), OrgID: a.OrgID, BrandID: b.ID, Livemode: a.Livemode, Month: month, CreatedBy: a.UserID,
		ExpiresAt: s.Now().Add(ReportShareTTL), CreatedAt: s.Now()}
	err = s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.CreateReportShare(ctx, sh, hash); err != nil {
			return err
		}
		return s.audit(ctx, tx, a, "report.share", id.Format(id.ReportShare, sh.ID), map[string]any{"brand": id.Format(id.Brand, b.ID), "month": month})
	})
	if err != nil {
		return "", nil, err
	}
	return token, sh, nil
}

// ReportShares lists a brand's open share links in the actor's mode.
func (s *Service) ReportShares(ctx context.Context, a Actor, brandID uuid.UUID) ([]*model.ReportShare, error) {
	if err := a.requireToRead(PermMembersWrite); err != nil {
		return nil, err
	}
	if _, err := s.Brand(ctx, a, brandID); err != nil {
		return nil, err
	}
	return s.store.ReportShares(ctx, a.OrgID, brandID, a.Livemode, s.Now())
}

// RevokeReportShare stops a share link working at once.
func (s *Service) RevokeReportShare(ctx context.Context, a Actor, shareID uuid.UUID) error {
	if err := a.require(PermMembersWrite); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.RevokeReportShare(ctx, a.OrgID, shareID, s.Now()); err != nil {
			return notFound(err, "shared report")
		}
		return s.audit(ctx, tx, a, "report.unshare", id.Format(id.ReportShare, shareID), nil)
	})
}

// SharedReport is the report a share link shows, for anyone holding the
// token: the brand's month, as a viewer of its org sees it. An unknown,
// revoked or expired link, or one of a suspended org, is not found.
func (s *Service) SharedReport(ctx context.Context, token string) (*Report, *model.ReportShare, error) {
	if token == "" {
		return nil, nil, errShareInvalid
	}
	sh, err := s.store.ReportShareByHash(ctx, authn.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errShareInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	if sh.RevokedAt != nil || !s.Now().Before(sh.ExpiresAt) {
		return nil, nil, errShareInvalid
	}
	o, err := s.store.Org(ctx, sh.OrgID)
	if err != nil {
		return nil, nil, notFound(err, "shared report")
	}
	if o.Status == model.OrgSuspended {
		return nil, nil, errShareInvalid
	}
	viewer := Actor{OrgID: sh.OrgID, Livemode: sh.Livemode, Role: model.RoleViewer, BrandID: &sh.BrandID, RequestID: "share",
		OrgStatus: o.Status}
	r, err := s.BrandReport(ctx, viewer, ReportInput{BrandID: sh.BrandID, Month: sh.Month})
	if err != nil {
		return nil, nil, err
	}
	return r, sh, nil
}
