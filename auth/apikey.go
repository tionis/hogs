package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
