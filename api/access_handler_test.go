package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/database"
)

func TestAccessGrantAPI(t *testing.T) {
	serverHandler, _ := mapProxyFixture(t, "game", nil)
	handler := NewAccessHandler(serverHandler.Store)
	body := bytes.NewBufferString(`{"subjectType":"group","subject":"Operators","capabilities":["start","command"]}`)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/servers/cog/access-grants", body)
	request = mux.SetURLVars(request, map[string]string{"serverName": "cog"})
	recorder := httptest.NewRecorder()
	handler.SetGrant(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/servers/cog/access-grants", nil)
	request = mux.SetURLVars(request, map[string]string{"serverName": "cog"})
	recorder = httptest.NewRecorder()
	handler.ListGrants(recorder, request)
	var grants []database.ServerAccessGrant
	if err := json.Unmarshal(recorder.Body.Bytes(), &grants); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, grant := range grants {
		if grant.Subject == "Operators" && grant.Effect == "allow" {
			found = true
		}
	}
	if !found {
		t.Fatalf("grants=%#v", grants)
	}
}

func TestGameIdentityAPIValidatesMinecraftName(t *testing.T) {
	serverHandler, _ := mapProxyFixture(t, "game", nil)
	handler := NewAccessHandler(serverHandler.Store)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/game-identities",
		bytes.NewBufferString(`{"userEmail":"player@example.test","gameType":"minecraft","username":"bad name"}`))
	recorder := httptest.NewRecorder()
	handler.SetGameIdentity(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
