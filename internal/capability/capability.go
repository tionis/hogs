package capability

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const DefaultLifetime = 90 * time.Second

var fallbackIDCounter atomic.Uint64

type Claims struct {
	Audience   string `json:"aud"`
	Subject    string `json:"sub"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	FilePath   string `json:"filePath,omitempty"`
	TargetPath string `json:"targetPath,omitempty"`
	MaxBytes   int64  `json:"maxBytes,omitempty"`
	IssuedAt   int64  `json:"iat"`
	Expires    int64  `json:"exp"`
	ID         string `json:"jti"`
}

func NewClaims(audience, subject, method, path, filePath string, maxBytes int64, lifetime time.Duration) Claims {
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	now := time.Now().UTC()
	return Claims{
		Audience: audience,
		Subject:  subject,
		Method:   method,
		Path:     path,
		FilePath: filePath,
		MaxBytes: maxBytes,
		IssuedAt: now.Unix(),
		Expires:  now.Add(lifetime).Unix(),
		ID:       randomID(),
	}
}

func Sign(secret []byte, claims Claims) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("capability secret must contain at least 32 bytes")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	var claims Claims
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return claims, errors.New("malformed capability")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, errors.New("malformed capability signature")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, errors.New("invalid capability signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return claims, errors.New("malformed capability payload")
	}
	if claims.Audience == "" || claims.Subject == "" || claims.Method == "" ||
		claims.Path == "" || claims.ID == "" || claims.Expires == 0 {
		return claims, errors.New("incomplete capability")
	}
	unix := now.UTC().Unix()
	if claims.IssuedAt > unix+30 {
		return claims, errors.New("capability is not active")
	}
	if claims.Expires < unix {
		return claims, errors.New("capability expired")
	}
	if claims.Expires-claims.IssuedAt > int64((5 * time.Minute).Seconds()) {
		return claims, errors.New("capability lifetime is too long")
	}
	return claims, nil
}

func Authorize(claims Claims, audience, method, path, filePath string) error {
	return AuthorizePaths(claims, audience, method, path, filePath, "")
}

// AuthorizePaths verifies a capability whose operation can be scoped to both a
// source file path and a destination path.
func AuthorizePaths(claims Claims, audience, method, path, filePath, targetPath string) error {
	if claims.Audience != audience {
		return fmt.Errorf("capability audience does not match")
	}
	if claims.Method != method {
		return fmt.Errorf("capability method does not match")
	}
	if claims.Path != path {
		return fmt.Errorf("capability path does not match")
	}
	if claims.FilePath != filePath {
		return fmt.Errorf("capability file path does not match")
	}
	if claims.TargetPath != targetPath {
		return fmt.Errorf("capability target path does not match")
	}
	return nil
}

func randomID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		fallback := fmt.Sprintf("%d:%d", time.Now().UnixNano(), fallbackIDCounter.Add(1))
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:12])
	}
	return hex.EncodeToString(value)
}
