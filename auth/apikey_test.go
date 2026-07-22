package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/tionis/hogs/database"
)

func TestRequireAPIKeyRole(t *testing.T) {
	database.APIKeyPepper = "api-key-test-pepper"
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	defer os.Remove(dbPath)
	plain, hash, prefix := GenerateAPIKey()
	if err := store.CreateAPIKey(&database.APIKey{Name: "gandalf", KeyHash: hash, KeyPrefix: prefix, Role: "admin", CreatedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	protected := APIKeyMiddleware(store, RequireAPIKeyRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	missing := httptest.NewRecorder()
	protected.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/inventory", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d", missing.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	allowed := httptest.NewRecorder()
	protected.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("valid key status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}
