package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

const csrfTokenLength = 32
const csrfFormField = "csrf_token"
const csrfHeaderField = "X-CSRF-Token"
const csrfCookieName = "hogs-csrf"

func generateCSRFToken() string {
	b := make([]byte, csrfTokenLength)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

func signCSRFToken(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyCSRFToken(token, signature, secret string) bool {
	expected := signCSRFToken(token, secret)
	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}

func CSRFMiddleware(secret string, isSecure func(*http.Request) bool, exemptPrefixes []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, prefix := range exemptPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}

		secure := isSecure(r)
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			// Only set a new CSRF cookie if one doesn't exist or is invalid.
			// Otherwise background GET requests (fetch, polling) would overwrite
			// the cookie and invalidate tokens already injected into forms.
			needsCookie := true
			if cookie, err := r.Cookie(csrfCookieName); err == nil {
				parts := strings.SplitN(cookie.Value, ".", 2)
				if len(parts) == 2 && verifyCSRFToken(parts[0], parts[1], secret) {
					needsCookie = false
				}
			}
			if needsCookie {
				setCSRFCookie(w, secret, secure)
			}
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(csrfCookieName)
		if err != nil {
			http.Error(w, "CSRF token missing", http.StatusForbidden)
			return
		}

		parts := strings.SplitN(cookie.Value, ".", 2)
		if len(parts) != 2 {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
			return
		}

		token := parts[0]
		signature := parts[1]
		if !verifyCSRFToken(token, signature, secret) {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
			return
		}

		submittedToken := r.FormValue(csrfFormField)
		if submittedToken == "" {
			submittedToken = r.Header.Get(csrfHeaderField)
		}

		if subtle.ConstantTimeCompare([]byte(submittedToken), []byte(token)) != 1 {
			http.Error(w, "CSRF token mismatch", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func setCSRFCookie(w http.ResponseWriter, secret string, secure bool) {
	token := generateCSRFToken()
	signature := signCSRFToken(token, secret)
	cookie := &http.Cookie{
		Name:     csrfCookieName,
		Value:    token + "." + signature,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	}
	http.SetCookie(w, cookie)
}

func CSRFTokenFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}
