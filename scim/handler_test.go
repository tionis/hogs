package scim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

func testHandler(t *testing.T) (*Handler, *database.Store) {
	t.Helper()
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	return NewHandler(store, &config.Config{
		OIDCAdminGroup: "Mage",
		OIDCUserGroup:  "Player",
	}, nil), store
}

func scimRequest(t *testing.T, method, path string, payload interface{}) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(method, path, bytes.NewReader(body))
}

func decodeSCIM(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid SCIM response %q: %v", recorder.Body.String(), err)
	}
	return result
}

func TestCreateUserAdoptsOIDCIdentityAndAuthentikUsername(t *testing.T) {
	handler, store := testHandler(t)
	user, err := store.CreateUser("old-address@example.test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserOIDCIdentity(user.ID, "https://auth.example.test/application/o/hogs/", "stable-subject", "old-name"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.CreateUser(recorder, scimRequest(t, http.MethodPost, "/scim/v2/Users", map[string]interface{}{
		"userName":    "authentik-name",
		"externalId":  "stable-subject",
		"displayName": "Authentik User",
		"active":      true,
	}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users=%#v, want one adopted identity", users)
	}
	if users[0].Email != "authentik-name" || users[0].ExternalID != "stable-subject" ||
		users[0].OIDCSubject != "stable-subject" || users[0].PreferredUsername != "authentik-name" {
		t.Fatalf("adopted user=%#v", users[0])
	}
}

func TestAuthentikGroupFilterAdoptionAndMembershipReplacement(t *testing.T) {
	handler, store := testHandler(t)
	user, err := store.CreateUser("player", "user")
	if err != nil {
		t.Fatal(err)
	}
	group := &database.SCIMGroup{DisplayName: "Mage"}
	if err := store.CreateSCIMGroup(group); err != nil {
		t.Fatal(err)
	}
	other := &database.SCIMGroup{ExternalID: "other-id", DisplayName: "Other"}
	if err := store.CreateSCIMGroup(other); err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRecorder()
	handler.CreateGroup(create, scimRequest(t, http.MethodPost, "/scim/v2/Groups", map[string]interface{}{
		"displayName": "Mage",
		"externalId":  "mage-id",
		"members": []map[string]string{
			{"value": fmt.Sprint(user.ID)},
		},
	}))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}

	filtered := httptest.NewRecorder()
	handler.ListGroups(filtered, httptest.NewRequest(http.MethodGet,
		`/scim/v2/Groups?filter=externalId%20eq%20%22mage-id%22`, nil))
	if filtered.Code != http.StatusOK {
		t.Fatalf("filter status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	response := decodeSCIM(t, filtered)
	if response["totalResults"] != float64(1) {
		t.Fatalf("filtered response=%#v", response)
	}

	adopted, err := store.GetSCIMGroupByExternalID("mage-id")
	if err != nil || adopted == nil || adopted.ID != group.ID {
		t.Fatalf("adopted group=%#v err=%v", adopted, err)
	}
	storedUser, _ := store.GetUserByID(user.ID)
	if storedUser.Role != "admin" {
		t.Fatalf("role=%q after Mage membership", storedUser.Role)
	}

	replace := httptest.NewRecorder()
	request := scimRequest(t, http.MethodPut, fmt.Sprintf("/scim/v2/Groups/%d", adopted.ID), map[string]interface{}{
		"displayName": "Mage",
		"externalId":  "mage-id",
		"members":     []interface{}{},
	})
	request = mux.SetURLVars(request, map[string]string{"id": fmt.Sprint(adopted.ID)})
	handler.ReplaceGroup(replace, request)
	if replace.Code != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", replace.Code, replace.Body.String())
	}
	members, _ := store.GetSCIMGroupMembers(adopted.ID)
	if len(members) != 0 {
		t.Fatalf("members=%#v, want empty replacement", members)
	}
	storedUser, _ = store.GetUserByID(user.ID)
	if storedUser.Role != "user" {
		t.Fatalf("role=%q after membership removal", storedUser.Role)
	}
}
