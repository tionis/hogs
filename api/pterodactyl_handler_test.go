package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tionis/hogs/backend"
)

type whitelistStatusBackend struct {
	status *backend.ServerStatus
	err    error
}

func (b *whitelistStatusBackend) Start(context.Context) error               { return nil }
func (b *whitelistStatusBackend) Stop(context.Context) error                { return nil }
func (b *whitelistStatusBackend) Restart(context.Context) error             { return nil }
func (b *whitelistStatusBackend) SendCommand(context.Context, string) error { return nil }
func (b *whitelistStatusBackend) Status(context.Context) (*backend.ServerStatus, error) {
	return b.status, b.err
}
func (b *whitelistStatusBackend) Name() string { return "test" }

func TestWhitelistBackendReadyRejectsStoppedServerClearly(t *testing.T) {
	recorder := httptest.NewRecorder()
	ready := whitelistBackendReady(recorder, context.Background(), &whitelistStatusBackend{
		status: &backend.ServerStatus{Online: false},
	})
	if ready {
		t.Fatal("stopped server was accepted for whitelist management")
	}
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "server is stopped") {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestWhitelistBackendReadyHidesTransportErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	ready := whitelistBackendReady(recorder, context.Background(), &whitelistStatusBackend{
		err: errors.New("dial tcp 127.0.0.1:25575: connect: connection refused"),
	})
	if ready {
		t.Fatal("unavailable server status was accepted for whitelist management")
	}
	if recorder.Code != http.StatusServiceUnavailable ||
		strings.Contains(recorder.Body.String(), "127.0.0.1") ||
		!strings.Contains(recorder.Body.String(), "Could not determine") {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
