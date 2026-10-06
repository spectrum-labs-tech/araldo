// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Notifications (ADR 0034): a person's, through a user token.

func (h *Handler) listNotifications(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	q := r.URL.Query()
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return badRequest("parameter_invalid", "limit", "limit must be between 1 and 100.")
		}
		limit = n
	}
	var after *uuid.UUID
	if v := q.Get("starting_after"); v != "" {
		u, err := id.Parse(id.Notification, v)
		if err != nil {
			return badRequest("parameter_invalid", "starting_after", "starting_after must be a ntf ID.")
		}
		after = &u
	}
	unread := false
	if v := q.Get("unread"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return badRequest("parameter_invalid", "unread", "unread is true or false.")
		}
		unread = b
	}
	ns, more, err := h.svc.ListNotifications(r.Context(), a, after, unread, limit)
	if err != nil {
		return err
	}
	out := make([]core.NotificationView, 0, len(ns))
	for _, n := range ns {
		out = append(out, core.ViewNotification(n, h.svc.BaseURL()))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/notifications"})
	return nil
}

func (h *Handler) readNotification(w http.ResponseWriter, r *http.Request) error {
	nid, err := pathID(r, id.Notification, "notification")
	if err != nil {
		return err
	}
	n, err := h.svc.ReadNotification(r.Context(), actor(r), nid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewNotification(n, h.svc.BaseURL()))
	return nil
}

func (h *Handler) readAllNotifications(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if a.UserID == nil {
		return personOnly(a)
	}
	if err := h.svc.ReadAllNotifications(r.Context(), *a.UserID); err != nil {
		return err
	}
	ok(w, http.StatusOK, map[string]any{"object": "notifications.read_all", "unread": 0})
	return nil
}
