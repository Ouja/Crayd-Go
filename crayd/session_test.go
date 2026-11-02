package crayd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type browser struct {
	t       *testing.T
	a       *App
	cookies map[string]*http.Cookie
}

func newBrowser(t *testing.T, a *App) *browser {
	return &browser{t: t, a: a, cookies: map[string]*http.Cookie{}}
}

func (b *browser) do(method, path string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	body := ""
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.a.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return w
}

func (b *browser) cookie() string {
	if c := b.cookies["crayd_session"]; c != nil {
		return c.Value
	}
	return ""
}

var tokenRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

func (b *browser) token(path string) string {
	b.t.Helper()
	m := tokenRe.FindStringSubmatch(b.do("GET", path, nil).Body.String())
	if m == nil {
		b.t.Fatalf("no CSRF field on %s", path)
	}
	return m[1]
}

type sCtl struct{ Controller }

func (c *sCtl) Index()      { c.View["msg"] = "user=" + c.UserID() }
func (c *sCtl) Plain()      { c.View["msg"] = "plain" } // never touches the session
func (c *sCtl) Form()       { c.View["msg"] = "form" }  // view prints {{ $.csrf.Field }}
func (c *sCtl) Put()        { c.Session().Set("k", c.Param("v", "")); c.View["msg"] = "ok" }
func (c *sCtl) Get()        { c.View["msg"] = "k=" + c.Session().Get("k") }
func (c *sCtl) PostSubmit() { c.View["msg"] = "submitted" }
func (c *sCtl) PostLogin()  { c.LoginAs(c.Param("id", "")); c.Redirect("/") }
func (c *sCtl) PostLogout() { c.LogoutUser(); c.Flash("bye"); c.Redirect("/") }
func (c *sCtl) SetFlash()   { c.Flash("saved!"); c.Redirect("/plain") }
func (c *sCtl) Secret() {
	if !c.RequireLogin("/login") {
		return
	}
	c.View["msg"] = "secret"
}
func (c *sCtl) Big()          { c.Session().Set("big", strings.Repeat("x", 5000)); c.View["msg"] = "big" }
func (c *sCtl) PostJSONData() { c.JSON(200, "ok") }

type apiCtl struct{ Controller }

func (c *apiCtl) PostHook() { c.JSON(200, "hooked") }

func sessionApp(t *testing.T, store SessionStore) *App {
	t.Helper()
	dir := t.TempDir()
	write := func(n, s string) { os.WriteFile(filepath.Join(dir, n+".html"), []byte(s), 0o644) }
	write("layout", `[{{ with .flash }}flash={{ . }};{{ end }}{{ template "content" . }}]`)
	for _, v := range []string{"index_index", "index_plain", "index_put", "index_get", "index_submit", "index_secret", "index_big"} {
		write(v, `{{ .msg }}`)
	}
	write("index_form", `{{ .msg }}{{ $.csrf.Field }}`)
	a := New(dir)
	a.Static("")
	a.Sessions(SessionOptions{Store: store})
	a.Register("index", &sCtl{})
	a.Register("api", &apiCtl{})
	a.SkipCSRF("api")
	return a
}

func stores() map[string]func() SessionStore {
	return map[string]func() SessionStore{
		"server": func() SessionStore { return ServerSessions(NewMemorySessionBackend()) },
		"cookie": func() SessionStore { return CookieSessions([]byte(strings.Repeat("k", 32))) },
	}
}

func TestSessionBasics(t *testing.T) {
	for name, mk := range stores() {
		t.Run(name, func(t *testing.T) {
			b := newBrowser(t, sessionApp(t, mk()))

			if w := b.do("GET", "/plain", nil); len(w.Result().Cookies()) != 0 {
				t.Fatal("a page that never touches the session must not set a cookie")
			}
			w := b.do("GET", "/put/v/hello", nil)
			ck := w.Result().Cookies()
			if len(ck) != 1 || !ck[0].HttpOnly || !ck[0].Secure || ck[0].SameSite != http.SameSiteLaxMode || ck[0].Path != "/" {
				t.Fatalf("bad cookie flags: %+v", ck)
			}
			if got := b.do("GET", "/get", nil).Body.String(); got != "[k=hello]" {
				t.Fatalf("read back %q", got)
			}
			if w := b.do("GET", "/get", nil); len(w.Result().Cookies()) != 0 {
				t.Fatal("unchanged session should not be re-saved on every request")
			}
		})
	}
}

func TestFlashShownOnce(t *testing.T) {
	for name, mk := range stores() {
		t.Run(name, func(t *testing.T) {
			b := newBrowser(t, sessionApp(t, mk()))
			if w := b.do("GET", "/set-flash", nil); w.Code != 303 {
				t.Fatalf("code %d", w.Code)
			}
			if got := b.do("GET", "/plain", nil).Body.String(); got != "[flash=saved!;plain]" {
				t.Fatalf("first view %q", got)
			}
			if got := b.do("GET", "/plain", nil).Body.String(); got != "[plain]" {
				t.Fatalf("flash shown twice: %q", got)
			}
			if b.cookie() != "" {
				t.Fatal("empty session should have been removed")
			}
		})
	}
}

func TestLoginRotatesSessionAndToken(t *testing.T) {
	for name, mk := range stores() {
		t.Run(name, func(t *testing.T) {
			a := sessionApp(t, mk())
			b := newBrowser(t, a)
			tok := b.token("/form")
			before := b.cookie()

			if w := b.do("POST", "/login", url.Values{"id": {"42"}, "_csrf": {tok}}); w.Code != 303 {
				t.Fatalf("login: %d %s", w.Code, w.Body.String())
			}
			after := b.cookie()
			if after == "" || after == before {
				t.Fatal("session ID was not renewed on login (session fixation)")
			}
			if got := b.do("GET", "/", nil).Body.String(); got != "[user=42]" {
				t.Fatalf("after login %q", got)
			}
			// the pre-login token must be dead
			if w := b.do("POST", "/submit", url.Values{"_csrf": {tok}}); w.Code != 403 {
				t.Fatalf("old CSRF token still accepted after login: %d", w.Code)
			}
			if name == "server" { // the pre-login session itself must be gone
				old := newBrowser(t, a)
				old.cookies["crayd_session"] = &http.Cookie{Name: "crayd_session", Value: before}
				if got := old.do("GET", "/", nil).Body.String(); got != "[user=]" {
					t.Fatalf("old session still valid: %q", got)
				}
			}
		})
	}
}

func TestLogout(t *testing.T) {
	a := sessionApp(t, ServerSessions(NewMemorySessionBackend()))
	b := newBrowser(t, a)
	b.do("POST", "/login", url.Values{"id": {"7"}, "_csrf": {b.token("/form")}})
	loggedIn := b.cookie()

	w := b.do("POST", "/logout", url.Values{"_csrf": {b.token("/form")}})
	if w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	if got := b.do("GET", "/", nil).Body.String(); got != "[flash=bye;user=]" {
		t.Fatalf("after logout %q", got)
	}
	// a stolen copy of the logged-in cookie must not work any more
	thief := newBrowser(t, a)
	thief.cookies["crayd_session"] = &http.Cookie{Name: "crayd_session", Value: loggedIn}
	if got := thief.do("GET", "/", nil).Body.String(); got != "[user=]" {
		t.Fatalf("logged-out session still valid: %q", got)
	}
}

func TestAttackerChosenSessionIDIgnored(t *testing.T) {
	a := sessionApp(t, ServerSessions(NewMemorySessionBackend()))
	b := newBrowser(t, a)
	planted := strings.Repeat("A", sessionIDLen) // looks valid, but the server never issued it
	b.cookies["crayd_session"] = &http.Cookie{Name: "crayd_session", Value: planted}
	b.do("GET", "/put/v/x", nil)
	if b.cookie() == planted {
		t.Fatal("server adopted an attacker-chosen session ID")
	}
}

func TestTamperedCookiesRejected(t *testing.T) {
	store := CookieSessions([]byte(strings.Repeat("k", 32)))
	ctx := context.Background()
	v, err := store.Save(ctx, "", map[string]string{"_uid": "1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Load(ctx, v); d["_uid"] != "1" {
		t.Fatal("roundtrip failed")
	}
	raw, _ := b64url.DecodeString(v)
	for _, pos := range []int{0, len(raw) / 2, len(raw) - 1} {
		bad := append([]byte(nil), raw...)
		bad[pos] ^= 1
		if d, _ := store.Load(ctx, b64url.EncodeToString(bad)); d != nil {
			t.Fatalf("flipped byte %d accepted", pos)
		}
	}
	other := CookieSessions([]byte(strings.Repeat("z", 32)))
	if d, _ := other.Load(ctx, v); d != nil {
		t.Fatal("cookie accepted with a different secret")
	}
	for _, junk := range []string{"", "x", "!!!", strings.Repeat("A", 200)} {
		if d, _ := store.Load(ctx, junk); d != nil {
			t.Fatalf("junk %q accepted", junk)
		}
	}
	expired, _ := store.Save(ctx, "", map[string]string{"a": "b"}, -time.Minute)
	if d, _ := store.Load(ctx, expired); d != nil {
		t.Fatal("expired cookie accepted")
	}
	if strings.Contains(v, "_uid") {
		t.Fatal("cookie data is not encrypted")
	}
}

func TestCookieTooBig(t *testing.T) {
	b := newBrowser(t, sessionApp(t, CookieSessions([]byte(strings.Repeat("k", 32)))))
	if w := b.do("GET", "/big", nil); w.Code != 200 || b.cookie() != "" {
		t.Fatalf("oversized session should be dropped (and logged), got code %d cookie %q", w.Code, b.cookie())
	}
}

func TestShortSecretPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	CookieSessions([]byte("short"))
}

func TestServerStoreKeepsOnlyHashes(t *testing.T) {
	backend := NewMemorySessionBackend()
	b := newBrowser(t, sessionApp(t, ServerSessions(backend)))
	b.do("GET", "/put/v/x", nil)
	id := b.cookie()
	for k := range backend.items {
		if strings.Contains(k, id) {
			t.Fatal("raw session ID stored in backend")
		}
	}
	if len(backend.items) != 1 {
		t.Fatalf("items = %d", len(backend.items))
	}
}

func TestCSRF(t *testing.T) {
	a := sessionApp(t, ServerSessions(NewMemorySessionBackend()))
	b := newBrowser(t, a)
	tok := b.token("/form")
	tok2 := b.token("/form")
	if tok == tok2 {
		t.Fatal("tokens should be masked differently on every render")
	}
	ok := func(w *httptest.ResponseRecorder) bool { return w.Code == 200 }

	cases := []struct {
		name    string
		form    url.Values
		headers []string
		allowed bool
	}{
		{"no token", url.Values{}, nil, false},
		{"valid token", url.Values{"_csrf": {tok}}, nil, true},
		{"second masked token", url.Values{"_csrf": {tok2}}, nil, true},
		{"token reused", url.Values{"_csrf": {tok}}, nil, true},
		{"header token", url.Values{}, []string{"X-CSRF-Token", tok}, true},
		{"garbage", url.Values{"_csrf": {"abc"}}, nil, false},
		{"truncated", url.Values{"_csrf": {tok[:40]}}, nil, false},
		{"cross-site with token", url.Values{"_csrf": {tok}}, []string{"Sec-Fetch-Site", "cross-site"}, false},
		{"same-site subdomain", url.Values{"_csrf": {tok}}, []string{"Sec-Fetch-Site", "same-site"}, false},
		{"same-origin", url.Values{"_csrf": {tok}}, []string{"Sec-Fetch-Site", "same-origin"}, true},
		{"foreign Origin", url.Values{"_csrf": {tok}}, []string{"Origin", "https://evil.example"}, false},
		{"null Origin", url.Values{"_csrf": {tok}}, []string{"Origin", "null"}, false},
		{"matching Origin", url.Values{"_csrf": {tok}}, []string{"Origin", "http://example.com"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := b.do("POST", "/submit", c.form, c.headers...)
			if ok(w) != c.allowed {
				t.Fatalf("allowed=%v, want %v (code %d %s)", ok(w), c.allowed, w.Code, w.Body.String())
			}
		})
	}

	// a token from another visitor's session is useless
	eve := newBrowser(t, a)
	eveTok := eve.token("/form")
	if w := b.do("POST", "/submit", url.Values{"_csrf": {eveTok}}); w.Code != 403 {
		t.Fatal("token from another session accepted")
	}
	// a visitor without a session cannot post at all
	if w := newBrowser(t, a).do("POST", "/submit", url.Values{"_csrf": {tok}}); w.Code != 403 {
		t.Fatal("token accepted without the matching session")
	}
	// JSON body with header token
	if w := b.do("POST", "/json-data", nil, "X-CSRF-Token", tok, "Content-Type", "application/json"); w.Code != 200 {
		t.Fatalf("header token on JSON request: %d", w.Code)
	}
	// exempt controller (webhooks / bearer-token API)
	if w := newBrowser(t, a).do("POST", "/api/hook", nil, "Sec-Fetch-Site", "cross-site"); w.Code != 200 {
		t.Fatalf("SkipCSRF controller blocked: %d", w.Code)
	}
	// GET is never blocked
	if w := b.do("GET", "/plain", nil, "Sec-Fetch-Site", "cross-site"); w.Code != 200 {
		t.Fatal("GET blocked")
	}
}

func TestTrustedOrigin(t *testing.T) {
	a := sessionApp(t, ServerSessions(NewMemorySessionBackend()))
	a.TrustOrigins("https://admin.example.com/")
	b := newBrowser(t, a)
	tok := b.token("/form")
	if w := b.do("POST", "/submit", url.Values{"_csrf": {tok}}, "Origin", "https://admin.example.com", "Sec-Fetch-Site", "same-site"); w.Code != 200 {
		t.Fatalf("trusted origin blocked: %d", w.Code)
	}
	if w := b.do("POST", "/submit", url.Values{}, "Origin", "https://admin.example.com"); w.Code != 403 {
		t.Fatal("trusted origin must still need a token")
	}
}

func TestOriginCheckWithoutSessions(t *testing.T) {
	a := newTestApp(t) // M1 app: no sessions
	if w := do(a, "POST", "/hello", "name=x"); w.Code != 303 {
		t.Fatalf("plain POST without sessions: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/hello", strings.NewReader("name=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-site POST without sessions: %d", w.Code)
	}
}

func TestRequireLogin(t *testing.T) {
	b := newBrowser(t, sessionApp(t, ServerSessions(NewMemorySessionBackend())))
	w := b.do("GET", "/secret?x=1", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/login?next=%2Fsecret%3Fx%3D1" {
		t.Fatalf("anonymous: %d %q", w.Code, w.Header().Get("Location"))
	}
	b.do("POST", "/login", url.Values{"id": {"1"}, "_csrf": {b.token("/form")}})
	if got := b.do("GET", "/secret", nil).Body.String(); got != "[secret]" {
		t.Fatalf("logged in: %q", got)
	}
}

func TestSafeRedirect(t *testing.T) {
	good := []string{"/", "/order", "/order/show/id/5?tab=x#y", "/a//b"}
	bad := []string{"", "order", "//evil.com", "/\\evil.com", "\\\\evil.com", "https://evil.com",
		"http:/evil.com", "javascript:alert(1)", "/\x00", "/a\nb", " /x"}
	for _, g := range good {
		if SafeRedirect(g, "/home") != g {
			t.Errorf("%q should be allowed", g)
		}
	}
	for _, b := range bad {
		if got := SafeRedirect(b, "/home"); got != "/home" {
			t.Errorf("%q should be rejected, got %q", b, got)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	a := newTestApp(t)
	big := strings.Repeat("a", maxBodyBytes+10)
	if w := do(a, "POST", "/hello", "name="+big); w.Code != 400 {
		t.Fatalf("oversized form: %d", w.Code)
	}
	// multipart (uploads) is where our own limit matters
	body := "--X\r\nContent-Disposition: form-data; name=\"f\"; filename=\"a.bin\"\r\n\r\n" + big + "\r\n--X--\r\n"
	r := httptest.NewRequest("POST", "/hello", strings.NewReader(body))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=X")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("oversized upload: %d", w.Code)
	}
	// a normal multipart form still works and its fields reach Param
	small := "--X\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nzed\r\n--X--\r\n"
	r = httptest.NewRequest("POST", "/hello", strings.NewReader(small))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=X")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/hello/name/zed" {
		t.Fatalf("multipart form: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestEmptiedSessionIsDeletedServerSide(t *testing.T) {
	backend := NewMemorySessionBackend()
	b := newBrowser(t, sessionApp(t, ServerSessions(backend)))
	b.do("GET", "/set-flash", nil)
	if len(backend.items) != 1 {
		t.Fatalf("items = %d", len(backend.items))
	}
	b.do("GET", "/plain", nil) // flash consumed -> session empty
	if len(backend.items) != 0 {
		t.Fatalf("emptied session left behind in the backend (%d items)", len(backend.items))
	}
}
