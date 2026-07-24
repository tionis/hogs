package auth

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns a normalized client address without a port. Forwarded
// headers are considered only when the deployment explicitly trusts its proxy.
func ClientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		for _, candidate := range []string{
			r.Header.Get("CF-Connecting-IP"),
			firstForwardedAddress(r.Header.Get("X-Forwarded-For")),
			r.Header.Get("X-Real-IP"),
		} {
			if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil {
				return ip.String()
			}
		}
	}

	host := strings.TrimSpace(r.RemoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

func firstForwardedAddress(value string) string {
	if index := strings.IndexByte(value, ','); index >= 0 {
		return value[:index]
	}
	return value
}
