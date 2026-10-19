package web

import "net/http"

// ContentSecurityPolicy is the policy for every page of the web client.
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' blob:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// securityHeaders returns the headers set on every response (ARCHITECTURE §7).
// A function returning an array value keeps the table immutable.
func securityHeaders() [6][2]string {
	return [...][2]string{
		{"Content-Security-Policy", ContentSecurityPolicy},
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"},
		{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
		{"Cross-Origin-Opener-Policy", "same-origin"},
		{"Cross-Origin-Resource-Policy", "same-origin"},
	}
}

// SecurityHeaders adds the global security headers before calling h.
func SecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		for _, kv := range securityHeaders() {
			hdr.Set(kv[0], kv[1])
		}
		h.ServeHTTP(w, r)
	})
}
