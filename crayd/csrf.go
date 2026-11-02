package crayd

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// CSRF protection has two layers, both on by default for POST/PUT/PATCH/DELETE:
//
//  1. Origin check (always): browsers send Sec-Fetch-Site/Origin; cross-origin
//     requests are rejected unless the origin is trusted with app.TrustOrigins.
//  2. Token check (when sessions are enabled): the request must carry the session's
//     token in the "_csrf" form field or the X-CSRF-Token header.
//
// In templates: {{ $.csrf.Field }} prints the hidden input; {{ $.csrf.Token }} the raw token
// (e.g. for a <meta> tag read by JavaScript).

const csrfKey = "_csrf"

// CSRF is placed in every view as .csrf. It creates the token lazily,
// so pages without forms don't start a session.
type CSRF struct{ s *Session }

// Token returns a fresh masked token. Masking (random pad XOR secret) makes every
// rendered token different, which defeats BREACH-style compression attacks.
func (c CSRF) Token() string {
	if c.s == nil {
		return ""
	}
	secret := c.s.csrfSecret()
	pad := randomBytes(len(secret))
	return b64url.EncodeToString(append(pad, xorBytes(pad, secret)...))
}

// Field returns the hidden form input carrying the token.
func (c CSRF) Field() template.HTML {
	t := c.Token()
	if t == "" {
		return ""
	}
	return template.HTML(`<input type="hidden" name="_csrf" value="` + t + `">`) // token is base64url: safe in HTML
}

func (s *Session) csrfSecret() []byte {
	if b, err := b64url.DecodeString(s.Get(csrfKey)); err == nil && len(b) == 32 {
		return b
	}
	b := randomBytes(32)
	s.Set(csrfKey, b64url.EncodeToString(b))
	return b
}

func (s *Session) validCSRF(token string) bool {
	secret, err := b64url.DecodeString(s.Get(csrfKey))
	if err != nil || len(secret) != 32 {
		return false
	}
	raw, err := b64url.DecodeString(token)
	if err != nil || len(raw) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare(xorBytes(raw[:32], raw[32:]), secret) == 1
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// TrustOrigins allows cross-origin unsafe requests from these exact origins,
// e.g. "https://admin.example.com". Use it when a proxy changes the Host header too.
func (a *App) TrustOrigins(origins ...string) {
	for _, o := range origins {
		a.trustedOrigins[strings.TrimRight(o, "/")] = true
	}
}

// SkipCSRF turns off both CSRF layers for these controllers. Only use it for
// endpoints that don't rely on cookies, such as an API using bearer tokens or webhooks.
func (a *App) SkipCSRF(controllers ...string) {
	for _, c := range controllers {
		a.csrfExempt[strings.ToLower(c)] = true
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions || m == http.MethodTrace
}

// sameOrigin follows the approach of Go 1.25's http.CrossOriginProtection.
func (a *App) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if a.trustedOrigins[origin] {
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// Older browser or not a browser: fall back to the Origin header.
	default:
		return false // cross-site or same-site (another subdomain)
	}
	if origin == "" {
		return true // no browser signals at all (curl, server-to-server); the token check still applies
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
}

// checkCSRF returns "" if the request may proceed, or the reason it was blocked.
func (a *App) checkCSRF(c *Controller, r *http.Request) string {
	if isSafeMethod(r.Method) || a.csrfExempt[c.Controller] {
		return ""
	}
	if !a.sameOrigin(r) {
		return "cross-origin request blocked"
	}
	if c.session == nil {
		return ""
	}
	token := r.PostForm.Get(csrfKey)
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	if !c.session.validCSRF(token) {
		return "invalid or missing CSRF token"
	}
	return ""
}
