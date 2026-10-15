// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Notifications (ADR 0034): a person's, through a user token.

func (h *Handler) listNotifications(w http.ResponseWriter, r *http.Request) error {
	pg, err := page(r, id.Notification)
	if err != nil {
		return err
	}
	unread := false
	if v := r.URL.Query().Get("unread"); v != "" {
		if unread, err = strconv.ParseBool(v); err != nil {
			return badRequest("parameter_invalid", "unread", "unread is true or false.")
		}
	}
	ns, more, err := h.svc.ListNotifications(r.Context(), actor(r), pg, unread)
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
	if err := h.svc.ReadAllNotifications(r.Context(), actor(r)); err != nil {
		return err
	}
	ok(w, http.StatusOK, map[string]any{"object": "notifications.read_all", "unread": 0})
	return nil
}
