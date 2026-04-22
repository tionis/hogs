package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateAndVerifyCSRFToken(t *testing.T) {
	secret := "test-secret-key"
	token := generateCSRFToken()
	if token == "" {
		t.Error("expected non-empty token")
	}
	signature := signCSRFToken(token, secret)
	if !verifyCSRFToken(token, signature, secret) {
		t.Error("expected token to verify")
	}
	if verifyCSRFToken(token, signature, "wrong-secret") {
		t.Error("expected token to fail with wrong secret")
	}
	if verifyCSRFToken("wrong-token", signature, secret) {
		t.Error("expected token to fail with wrong token")
	}
}

func TestCSRFMiddlewareExemptPaths(t *testing.T) {
	secret := "test-secret-key"
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := CSRFMiddleware(secret, func(r *http.Request) bool { return false }, []string{"/api/", "/agent/"}, handler)

	req := httptest.NewRequest(http.MethodPost, "/api/servers", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if !called {
		t.Error("expected handler to be called for exempt path")
	}
}

func TestCSRFMiddlewareGETPasses(t *testing.T) {
	secret := "test-secret-key"
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := CSRFMiddleware(secret, func(r *http.Request) bool { return false }, nil, handler)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if !called {
		t.Error("expected handler to be called for GET request")
	}

	cookie := w.Result().Cookies()
	found := false
	for _, c := range cookie {
		if c.Name == csrfCookieName {
			found = true
		}
	}
	if !found {
		t.Error("expected CSRF cookie to be set on GET request")
	}
}

func TestCSRFMiddlewarePOSTRequiresToken(t *testing.T) {
	secret := "test-secret-key"
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	mw := CSRFMiddleware(secret, func(r *http.Request) bool { return false }, nil, handler)

	req := httptest.NewRequest(http.MethodPost, "/admin/servers/add", nil)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if called {
		t.Error("expected handler NOT to be called without CSRF token")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestCSRFMiddlewarePOSTWithValidToken(t *testing.T) {
	secret := "test-secret-key"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := CSRFMiddleware(secret, func(r *http.Request) bool { return false }, nil, handler)

	token := generateCSRFToken()
	signature := signCSRFToken(token, secret)
	cookieValue := token + "." + signature

	req := httptest.NewRequest(http.MethodPost, "/admin/servers/add", nil)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: cookieValue})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(csrfHeaderField, token)

	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestCSRFTokenFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	token := CSRFTokenFromRequest(req)
	if token != "" {
		t.Error("expected empty token for request without cookie")
	}

	token = generateCSRFToken()
	signature := signCSRFToken(token, "secret")
	cookieValue := token + "." + signature
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: csrfCookieName, Value: cookieValue})
	got := CSRFTokenFromRequest(req2)
	if got != token {
		t.Errorf("expected token %q, got %q", token, got)
	}
}
