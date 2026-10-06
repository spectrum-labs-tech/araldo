// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/smtpmail"
	"github.com/spectrum-labs-tech/araldo/internal/store"
)

// Notification email (ADR 0034): queued with the notification, sent by the
// notifications.send task.

const (
	// notificationEmailBatch is how many emails one run claims.
	notificationEmailBatch = 100
	// notificationEmailLease is how long a claimed email is left to its
	// worker before another may take it.
	notificationEmailLease = 10 * time.Minute
	// notificationEmailsPerHour caps the notification emails a person gets.
	notificationEmailsPerHour = 30
	// notificationEmailAttempts is how many tries an email gets.
	notificationEmailAttempts = 6
)

// notificationEmailRetry is the wait after each failed try: about a day in
// all.
var notificationEmailRetry = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// SendNotificationEmails sends the notification emails due.
func (s *Service) SendNotificationEmails(ctx context.Context) (int, error) {
	return s.sendNotificationEmails(ctx, nil)
}

// sendNotificationEmails sends one person's due emails, or (userID nil)
// everyone's.
func (s *Service) sendNotificationEmails(ctx context.Context, userID *uuid.UUID) (int, error) {
	now := s.Now()
	due, err := s.store.ClaimNotificationEmails(ctx, userID, now, now.Add(notificationEmailLease), notificationEmailBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	digests := map[uuid.UUID][]store.NotificationEmailDue{} // org notifications for a daily summary, by person
	var order []uuid.UUID
	for _, d := range due {
		if d.Digest && d.OrgID != nil {
			if digests[d.UserID] == nil {
				order = append(order, d.UserID)
			}
			digests[d.UserID] = append(digests[d.UserID], d)
			continue
		}
		status, reason, next, err := s.deliverNotificationEmail(ctx, now, d)
		if err != nil {
			return n, err
		}
		if status == "sent" {
			n++
		}
		if err := s.store.FinishNotificationEmail(ctx, d.ID, status, reason, s.Now(), next); err != nil {
			return n, err
		}
	}
	for _, u := range order {
		group := digests[u]
		status, reason, next, err := s.deliverDigest(ctx, now, group)
		if err != nil {
			return n, err
		}
		if status == "sent" {
			n++
		}
		for _, d := range group {
			if err := s.store.FinishNotificationEmail(ctx, d.ID, status, reason, s.Now(), next); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// deliverDigest sends a person's daily summary: their org notifications
// due, in one email, as deliverNotificationEmail sends one.
func (s *Service) deliverDigest(ctx context.Context, now time.Time, group []store.NotificationEmailDue) (status, reason string, next *time.Time,
	err error) {
	if s.cfg.Mail == nil {
		return "suppressed", "the server sends no email", nil, nil
	}
	first := group[0]
	c := mailContent{To: first.To, ButtonLabel: "Open your notifications", ButtonURL: s.cfg.BaseURL + "/notifications",
		Subject: fmt.Sprintf("Araldo: %d things since yesterday", len(group))}
	if len(group) == 1 {
		c.Subject = "Araldo: " + first.Subject
	}
	for _, d := range group {
		line := d.OrgName + ": " + d.Subject
		if d.Body != "" {
			line += ". " + d.Body
		}
		c.Paragraphs = append(c.Paragraphs, line)
	}
	unsub, err := s.signUnsubscribe(ctx, "all|"+first.UserID.String())
	if err != nil {
		return "", "", nil, err
	}
	c.Footer = "Your daily summary of Araldo notifications. Change what you get, or have them as they happen: " + s.cfg.BaseURL +
		"/notifications/settings · Stop all of these emails: " + unsub
	m, err := c.message()
	if err != nil {
		return "", "", nil, err
	}
	m.Headers = map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
	attempts := 0
	for _, d := range group {
		attempts = max(attempts, d.Attempts)
	}
	if err := s.cfg.Mail.Send(ctx, m); err != nil {
		s.log.WarnContext(ctx, "sending a daily summary failed", "attempt", attempts, "err", err)
		reason = truncate(err.Error(), 500)
		if smtpmail.IsPermanent(err) || attempts >= notificationEmailAttempts {
			return "failed", reason, nil, nil
		}
		return "queued", reason, ptr(now.Add(notificationEmailRetry[min(attempts, len(notificationEmailRetry))-1])), nil
	}
	return "sent", "", nil, nil
}

// deliverNotificationEmail sends one claimed email, unless the server sends
// none or the person had 30 this hour, and says what became of it: sent,
// suppressed, queued again for next, or failed. Only storage errors are
// returned.
func (s *Service) deliverNotificationEmail(ctx context.Context, now time.Time, d store.NotificationEmailDue) (status, reason string, next *time.Time,
	err error) {
	if s.cfg.Mail == nil {
		return "suppressed", "the server sends no email", nil, nil
	}
	hour, err := s.store.NotificationEmailsSent(ctx, d.UserID, now.Add(-time.Hour)) // this run's sends are recorded as they go
	if err != nil {
		return "", "", nil, err
	}
	if hour >= notificationEmailsPerHour {
		return "suppressed", "more than 30 notification emails in an hour", nil, nil
	}
	if err := s.sendNotificationEmail(ctx, d); err != nil {
		s.log.WarnContext(ctx, "sending a notification email failed", "notification", d.ID, "attempt", d.Attempts, "err", err)
		reason = truncate(err.Error(), 500)
		if smtpmail.IsPermanent(err) || d.Attempts >= notificationEmailAttempts {
			return "failed", reason, nil, nil
		}
		return "queued", reason, ptr(now.Add(notificationEmailRetry[min(d.Attempts, len(notificationEmailRetry))-1])), nil
	}
	return "sent", "", nil, nil
}

// sendNotificationEmail renders and sends one notification's email. An
// org notification carries a one-click unsubscribe (RFC 8058) from its
// type in its org; an account notice cannot be turned off.
func (s *Service) sendNotificationEmail(ctx context.Context, d store.NotificationEmailDue) error {
	t, _ := notificationType(d.Type)
	c := mailContent{To: d.To, Subject: d.Subject, ButtonLabel: "Open in Araldo", ButtonURL: s.cfg.BaseURL + "/notifications/" + d.ID.String()}
	if d.Body != "" {
		c.Paragraphs = append(c.Paragraphs, d.Body)
	}
	var headers map[string]string
	if d.OrgID == nil {
		c.Footer = "This is about your Araldo account, so it is always sent."
	} else {
		unsub, err := s.UnsubscribeURL(ctx, d.UserID, *d.OrgID, d.Type)
		if err != nil {
			return err
		}
		c.Footer = "You get this because you are in " + d.OrgName + " and asked for “" + t.Name + "” by email. Stop these emails: " + unsub +
			" · All your settings: " + s.cfg.BaseURL + "/notifications/settings"
		headers = map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}
	}
	m, err := c.message()
	if err != nil {
		return err
	}
	m.Headers = headers
	return s.cfg.Mail.Send(ctx, m)
}

// Unsubscribe links, signed so they work without signing in and cannot be
// made for anyone else: a notification type for a person in an org
// ("user|org|type"), or a brand's monthly report for an address
// ("report|org|brand|email", ADR 0026).

const unsubscribeLabel = "araldo notification unsubscribe v1"

var errUnsubscribeInvalid = &apperr.Error{Kind: apperr.KindNotFound, Code: "unsubscribe_link_invalid",
	Message: "This link is not valid. Change your email settings under Notifications in Araldo."}

func (s *Service) signUnsubscribe(ctx context.Context, payload string) (string, error) {
	key, err := s.keys.Derive(ctx, unsubscribeLabel)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return s.cfg.BaseURL + "/unsubscribe/" + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// UnsubscribeURL is the link a notification email carries to stop that
// type's email for the person in the org.
func (s *Service) UnsubscribeURL(ctx context.Context, userID, orgID uuid.UUID, typ string) (string, error) {
	return s.signUnsubscribe(ctx, userID.String()+"|"+orgID.String()+"|"+typ)
}

// reportUnsubscribeURL is the link a monthly report email carries to stop
// it for that address.
func (s *Service) reportUnsubscribeURL(ctx context.Context, orgID, brandID uuid.UUID, email string) (string, error) {
	return s.signUnsubscribe(ctx, "report|"+orgID.String()+"|"+brandID.String()+"|"+email)
}

// UnsubscribeTarget is what an unsubscribe link turns off: a notification
// type (Type) for a person, or a brand's monthly report (Brand) for an
// address.
type UnsubscribeTarget struct {
	OrgID   uuid.UUID
	OrgName string
	UserID  uuid.UUID
	Type    NotificationType
	Brand   *model.Brand
	Email   string
	// All turns off email for every org notification type, in every org of
	// the person's (a daily summary's link).
	All bool
}

// CheckUnsubscribe reads an unsubscribe link.
func (s *Service) CheckUnsubscribe(ctx context.Context, token string) (*UnsubscribeTarget, error) {
	rawPayload, rawMAC, ok := strings.Cut(token, ".")
	payload, err1 := base64.RawURLEncoding.DecodeString(rawPayload)
	got, err2 := base64.RawURLEncoding.DecodeString(rawMAC)
	if !ok || err1 != nil || err2 != nil {
		return nil, errUnsubscribeInvalid
	}
	key, err := s.keys.Derive(ctx, unsubscribeLabel)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return nil, errUnsubscribeInvalid
	}
	parts := strings.Split(string(payload), "|")
	var u UnsubscribeTarget
	switch {
	case len(parts) == 2 && parts[0] == "all":
		userID, err := uuid.Parse(parts[1])
		if err != nil {
			return nil, errUnsubscribeInvalid
		}
		if _, err := s.store.User(ctx, userID); err != nil {
			return nil, errUnsubscribeInvalid
		}
		return &UnsubscribeTarget{UserID: userID, All: true}, nil
	case len(parts) == 4 && parts[0] == "report":
		orgID, err1 := uuid.Parse(parts[1])
		brandID, err2 := uuid.Parse(parts[2])
		if err1 != nil || err2 != nil {
			return nil, errUnsubscribeInvalid
		}
		b, err := s.store.Brand(ctx, orgID, brandID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errUnsubscribeInvalid
		}
		if err != nil {
			return nil, err
		}
		u = UnsubscribeTarget{OrgID: orgID, Brand: b, Email: parts[3]}
	case len(parts) == 3:
		userID, err1 := uuid.Parse(parts[0])
		orgID, err2 := uuid.Parse(parts[1])
		t, known := notificationType(parts[2])
		if err1 != nil || err2 != nil || !known || t.Account {
			return nil, errUnsubscribeInvalid
		}
		u = UnsubscribeTarget{OrgID: orgID, UserID: userID, Type: t}
	default:
		return nil, errUnsubscribeInvalid
	}
	o, err := s.store.Org(ctx, u.OrgID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnsubscribeInvalid
	}
	if err != nil {
		return nil, err
	}
	u.OrgName = o.Name
	return &u, nil
}

// Unsubscribe does what an unsubscribe link says: turns off a type's email
// for the person in the org (the dashboard keeps showing it as before), or
// takes the address off the brand's monthly report.
func (s *Service) Unsubscribe(ctx context.Context, token string) (*UnsubscribeTarget, error) {
	u, err := s.CheckUnsubscribe(ctx, token)
	if err != nil {
		return nil, err
	}
	if u.All {
		var types []string
		for _, t := range NotificationTypes {
			if !t.Account {
				types = append(types, t.Key)
			}
		}
		return u, s.store.TurnOffOrgEmails(ctx, u.UserID, types)
	}
	if u.Brand != nil {
		if err := s.store.RemoveReportRecipient(ctx, u.OrgID, u.Brand.ID, u.Email); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return u, nil
	}
	prefs, err := s.store.UserNotificationPrefs(ctx, u.UserID, u.OrgID)
	if err != nil {
		return nil, err
	}
	inApp := true
	if p, ok := prefs[u.Type.Key]; ok {
		inApp = p.InApp
	}
	if err := s.store.SetNotificationPref(ctx, u.UserID, u.OrgID, model.NotificationPref{Type: u.Type.Key, InApp: inApp, Email: false}); err != nil {
		if errors.Is(err, store.ErrReferenced) {
			return nil, errUnsubscribeInvalid // the person or org is gone
		}
		return nil, err
	}
	return u, nil
}
