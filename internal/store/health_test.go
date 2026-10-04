// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Nothing listens on port 1, so this is a database that is down.
const downDSN = "postgres://araldo:x@127.0.0.1:1/araldo?connect_timeout=1"

func TestOpenDoesNotWaitForTheDatabase(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), downDSN)
	if err != nil {
		t.Fatalf("Open with the database down = %v, want a store", err)
	}
	defer st.Close()
	err = st.Ping(t.Context())
	if err == nil {
		t.Fatal("Ping succeeded with nothing listening")
	}
	if !Unavailable(err) {
		t.Fatalf("Unavailable(%v) = false, want true", err)
	}
	if Unavailable(errors.New("syntax error at or near")) || Unavailable(ErrNotFound) {
		t.Fatal("Unavailable is true for errors that are not about reaching the database")
	}
	if _, err := Open(t.Context(), "postgres://%zz"); err == nil {
		t.Fatal("Open accepted a malformed URL")
	}
}

func TestHealthMonitorNoticesTheDatabaseIsDown(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), downDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewHealthMonitor(st, slog.New(slog.NewTextHandler(io.Discard, nil)), 50*time.Millisecond)
	if !m.Healthy() {
		t.Fatal("a new monitor should start healthy")
	}
	m.Start(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for m.Healthy() {
		if time.Now().After(deadline) {
			t.Fatal("the monitor never noticed the database was down")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
