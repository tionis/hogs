package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/gametypes"
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

type identityRetryWhitelistBackend struct {
	requests []backend.WhitelistRequest
}

func (b *identityRetryWhitelistBackend) Whitelist(_ context.Context, request backend.WhitelistRequest) (*backend.WhitelistResult, error) {
	b.requests = append(b.requests, request)
	if request.ExternalID == "" {
		return nil, &backend.WhitelistError{Code: "identity_required", Message: "UUID required"}
	}
	return &backend.WhitelistResult{
		Mode: "offline",
		Entries: []backend.WhitelistEntry{{
			UUID: request.ExternalID, Name: request.Username,
		}},
	}, nil
}

func TestStructuredWhitelistResolvesIdentityOnlyWhenWorkerRequiresIt(t *testing.T) {
	handler := &PterodactylHandler{IdentityHTTPClient: http.DefaultClient}
	driver, _ := gametypes.Embedded("minecraft")
	driver.ResolveIdentity = func(context.Context, *http.Client, string) (gametypes.ResolvedIdentity, error) {
		return gametypes.ResolvedIdentity{
			Username: "CanonicalPlayer", ExternalID: "123456781234123412341234567890ab",
		}, nil
	}
	worker := &identityRetryWhitelistBackend{}
	result, resolved, err := handler.structuredWhitelist(context.Background(), worker, driver, backend.WhitelistRequest{
		Operation: "add", Username: "canonicalplayer",
	})
	if err != nil || resolved == nil || len(worker.requests) != 2 || result.Mode != "offline" {
		t.Fatalf("result=%#v resolved=%#v requests=%#v err=%v", result, resolved, worker.requests, err)
	}
	if worker.requests[1].Username != "CanonicalPlayer" ||
		worker.requests[1].ExternalID != "123456781234123412341234567890ab" {
		t.Fatalf("retry did not carry verified profile: %#v", worker.requests[1])
	}
}
