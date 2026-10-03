// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/model"
)

func TestDeriveIssueStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	const (
		queued   = model.DeliveryQueued
		handed   = model.DeliveryHandedOff
		sent     = model.DeliverySent
		canceled = model.DeliveryCanceled
		failed   = model.DeliveryFailed
		attn     = model.DeliveryNeedsAttention
	)
	tests := []struct {
		name   string
		status model.IssueStatus
		sendAt time.Time
		ds     []model.DeliveryStatus
		want   model.IssueStatus
	}{
		{"a draft stays a draft", model.IssueDraft, later, []model.DeliveryStatus{model.DeliveryDraft}, model.IssueDraft},
		{"waiting for approval stays so", model.IssuePendingApproval, later, []model.DeliveryStatus{model.DeliveryHeld}, model.IssuePendingApproval},
		{"queued", model.IssueScheduled, later, []model.DeliveryStatus{queued, handed}, model.IssueScheduled},
		{"handed off, before the send time", model.IssueScheduled, later, []model.DeliveryStatus{handed}, model.IssueScheduled},
		{"handed off, after the send time", model.IssueScheduled, earlier, []model.DeliveryStatus{handed}, model.IssueSending},
		{"one sent, one to go", model.IssueScheduled, later, []model.DeliveryStatus{sent, queued}, model.IssueSending},
		{"all sent", model.IssueSending, earlier, []model.DeliveryStatus{sent, sent}, model.IssueSent},
		{"sent, the rest canceled", model.IssueSending, earlier, []model.DeliveryStatus{sent, canceled}, model.IssueSent},
		{"sent and failed", model.IssueSending, earlier, []model.DeliveryStatus{sent, failed}, model.IssuePartiallySent},
		{"sent and stuck", model.IssueSending, earlier, []model.DeliveryStatus{sent, attn}, model.IssuePartiallySent},
		{"all canceled", model.IssueScheduled, later, []model.DeliveryStatus{canceled, canceled}, model.IssueCanceled},
		{"nothing sent", model.IssueScheduled, earlier, []model.DeliveryStatus{failed, canceled}, model.IssueFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			is := &model.Issue{Status: tt.status, SendAt: &tt.sendAt}
			for _, s := range tt.ds {
				is.Deliveries = append(is.Deliveries, model.IssueDelivery{Status: s})
			}
			if got := deriveIssueStatus(is, now); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}
