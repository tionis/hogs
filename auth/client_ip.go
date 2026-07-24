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
			firstForwardedAddress(r.Header.Get("X-Forwarded-For")),
			r.Header.Get("CF-Connecting-IP"),
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

// ClientCountry returns an ISO 3166-1 alpha-2 code supplied by a trusted edge.
// HOGS does not accept country assertions directly from untrusted clients.
func ClientCountry(r *http.Request, trustProxyHeaders bool) string {
	if !trustProxyHeaders {
		return ""
	}
	for _, header := range []string{"CF-IPCountry", "CDN-Country-Code", "X-Country-Code"} {
		country := strings.ToUpper(strings.TrimSpace(r.Header.Get(header)))
		if len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' &&
			country[1] >= 'A' && country[1] <= 'Z' {
			return country
		}
	}
	return ""
}

func firstForwardedAddress(value string) string {
	if index := strings.IndexByte(value, ','); index >= 0 {
		return value[:index]
	}
	return value
}
