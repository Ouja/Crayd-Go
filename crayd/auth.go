package crayd

import (
	"net/http"
	"net/url"
	"strings"
)

const userKey = "_uid"

// LoginAs marks the session as belonging to userID. It renews the session ID and
// the CSRF token, so a session planted by an attacker before login is useless after it.
func (c *Controller) LoginAs(userID string) {
	s := c.Session()
	s.RenewID()
	s.Delete(csrfKey)
	s.Set(userKey, userID)
}

// LogoutUser ends the session completely. Flash messages set afterwards still work.
func (c *Controller) LogoutUser() { c.Session().Clear() }

// UserID returns the logged-in user's ID, or "" if nobody is logged in.
func (c *Controller) UserID() string {
	if c.session == nil {
		return ""
	}
	return c.session.Get(userKey)
}

// RequireLogin redirects anonymous users to loginPath (with ?next= for GET requests)
// and returns false. Typical use, in PreDispatch:
//
//	func (c *Admin) PreDispatch() { c.RequireLogin("/auth/login") }
func (c *Controller) RequireLogin(loginPath string) bool {
	if c.UserID() != "" {
		return true
	}
	target := loginPath
	if c.R.Method == http.MethodGet {
		target += "?next=" + url.QueryEscape(c.R.URL.RequestURI())
	}
	c.Redirect(target)
	return false
}

// SafeRedirect returns target if it is a local path on this site, otherwise fallback.
// Use it for ?next= style parameters to prevent open redirects.
func SafeRedirect(target, fallback string) string {
	if target == "" || target[0] != '/' || strings.HasPrefix(target, "//") {
		return fallback
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f || r == '\\' { // browsers treat \ as /, so "/\evil.com" is external
			return fallback
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return fallback
	}
	return target
}
