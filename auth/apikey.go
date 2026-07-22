package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tionis/hogs/database"
)

const apiKeyPrefix = "hogs_"

type APIKeyAuthenticator struct {
	Store *database.Store
}

func NewAPIKeyAuthenticator(store *database.Store) *APIKeyAuthenticator {
	return &APIKeyAuthenticator{Store: store}
}

func GenerateAPIKey() (plain, hash, prefix string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", ""
	}
	plain = apiKeyPrefix + hex.EncodeToString(b)
	hash = database.HashAPIKey(plain)
	prefix = plain[:8]
	return plain, hash, prefix
}

// BootstrapAdminAPIKey installs or rotates one named admin identity from a
// deployment secret. The plaintext credential is never written to the DB.
func BootstrapAdminAPIKey(store *database.Store, name, plain string) (bool, error) {
	if plain == "" {
		return false, nil
	}
	if name == "" || !strings.HasPrefix(plain, apiKeyPrefix) || len(plain) < 8 {
		return false, fmt.Errorf("bootstrap API key requires a name and hogs_ credential")
	}
	hash := database.HashAPIKey(plain)
	existing, err := store.GetAPIKeyByHash(hash)
	if err != nil {
		return false, err
	}
	if existing != nil && existing.Name == name && existing.Role == "admin" {
		return false, nil
	}
	key := &database.APIKey{
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: plain[:8],
		Role:      "admin",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := store.ReplaceAPIKeyByName(key); err != nil {
		return false, err
	}
	return true, nil
}

func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (*database.APIKey, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, nil
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, nil
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if !strings.HasPrefix(token, apiKeyPrefix) {
		return nil, nil
	}

	keyHash := database.HashAPIKey(token)
	key, err := a.Store.GetAPIKeyByHash(keyHash)
	if err != nil {
		return nil, err
	}

	if key == nil {
		return nil, nil
	}

	if key.ExpiresAt != nil && *key.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, *key.ExpiresAt)
		if err == nil && time.Now().UTC().After(expiresAt) {
			return nil, nil
		}
	}

	a.Store.UpdateAPIKeyLastUsed(key.ID)

	return key, nil
}

type contextKey string

const apiKeyContextKey contextKey = "api_key"

func APIKeyMiddleware(store *database.Store, next http.Handler) http.Handler {
	auth := NewAPIKeyAuthenticator(store)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := auth.Authenticate(r)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if key != nil {
			ctx := context.WithValue(r.Context(), apiKeyContextKey, key)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func GetAPIKeyFromContext(r *http.Request) *database.APIKey {
	key, _ := r.Context().Value(apiKeyContextKey).(*database.APIKey)
	return key
}

// RequireAPIKeyRole protects machine-oriented endpoints without depending on
// an interactive OIDC session. This is the authentication boundary used by
// inventory reconciliation clients such as Gandalf.
func RequireAPIKeyRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := GetAPIKeyFromContext(r)
			if key == nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if _, ok := allowed[key.Role]; !ok {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
