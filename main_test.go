package main

import (
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"crayd-go/crayd"
	"crayd-go/crayd/password"
	"crayd-go/store"
)

func init() { password.Iterations = 1000 } // fast tests; production uses 600,000

// client is a real browser-like HTTPS client with a cookie jar.
type client struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newClient(t *testing.T, srv *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	c := *srv.Client() // copy: srv.Client() returns one shared client
	c.Jar = jar
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{t: t, base: srv.URL, c: &c}
}

type resp struct {
	code     int
	body     string
	location string
}

func (cl *client) do(method, path string, form url.Values) resp {
	cl.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, cl.base+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := cl.c.Do(r)
	if err != nil {
		cl.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, string(b), res.Header.Get("Location")}
}

var tokenRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post fetches formPage, takes its CSRF token and submits form to action, like a browser.
func (cl *client) post(formPage, action string, form url.Values) resp {
	cl.t.Helper()
	m := tokenRe.FindStringSubmatch(cl.do("GET", formPage, nil).body)
	if m == nil {
		cl.t.Fatalf("no CSRF token on %s", formPage)
	}
	form.Set("_csrf", m[1])
	return cl.do("POST", action, form)
}

func expect(t *testing.T, r resp, code int, contains ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status %d, want %d; body:\n%s", r.code, code, r.body)
	}
	for _, s := range contains {
		if !strings.Contains(r.body, s) {
			t.Fatalf("body missing %q:\n%s", s, r.body)
		}
	}
}

func testServer(t *testing.T) *httptest.Server {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	srv := httptest.NewTLSServer(buildApp(Deps{
		Cache:    store.NewMemoryCache(),
		Sessions: crayd.ServerSessions(crayd.NewMemorySessionBackend()),
		Users:    store.NewMemoryUsers(),
		Orders:   store.NewMemoryOrders(),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthAndOrderFlow(t *testing.T) {
	srv := testServer(t)
	ada := newClient(t, srv)

	// orders need a login; we come back to the page we wanted afterwards
	r := ada.do("GET", "/order/new", nil)
	expect(t, r, 303)
	if r.location != "/auth/login?next=%2Forder%2Fnew" {
		t.Fatalf("redirect to %q", r.location)
	}

	// register: validation, then success
	expect(t, ada.post("/auth/register", "/auth/register", url.Values{"email": {"not-an-email"}, "password": {"short"}}),
		422, "Enter a valid email", "at least 8 characters", `value="not-an-email"`)
	r = ada.post("/auth/register", "/auth/register", url.Values{"email": {" Ada@Example.com "}, "password": {"correct horse"}})
	expect(t, r, 303)
	expect(t, ada.do("GET", "/order", nil), 200, "Welcome, ada@example.com", "Sign out", "No orders yet.")
	expect(t, ada.do("GET", "/order", nil), 200) // flash only once
	if strings.Contains(ada.do("GET", "/order", nil).body, "Welcome") {
		t.Fatal("flash shown twice")
	}

	// duplicate registration from another browser
	bob := newClient(t, srv)
	expect(t, bob.post("/auth/register", "/auth/register", url.Values{"email": {"ada@example.com"}, "password": {"whatever123"}}),
		422, "already registered")

	// orders, with CSRF tokens taken from the forms
	r = ada.post("/order/new", "/order/checkout", url.Values{"customer": {"Ada"}, "name1": {"Keyboard"}, "qty1": {"2"}, "price1": {"60"}})
	expect(t, r, 303)
	expect(t, ada.do("GET", r.location, nil), 200, "Total: $108.00")
	expect(t, ada.do("GET", "/order/new", nil), 200, `value="ada@example.com"`) // customer prefilled

	// posting without a token is refused
	expect(t, ada.do("POST", "/order/checkout", url.Values{"customer": {"x"}, "name1": {"x"}, "qty1": {"1"}, "price1": {"1"}}), 403)
	expect(t, ada.do("POST", "/order/delete/id/1", url.Values{}), 403)

	// logout, then the orders are closed again
	expect(t, ada.post("/order", "/auth/logout", url.Values{}), 303)
	expect(t, ada.do("GET", "/", nil), 200, "You are signed out", "Sign in")
	expect(t, ada.do("GET", "/order", nil), 303)

	// login: wrong password, unknown user, then correct with ?next=
	expect(t, ada.post("/auth/login", "/auth/login", url.Values{"email": {"ada@example.com"}, "password": {"nope nope"}}), 401, "Wrong email or password", `value="ada@example.com"`)
	expect(t, ada.post("/auth/login", "/auth/login", url.Values{"email": {"ghost@example.com"}, "password": {"nope nope"}}), 401, "Wrong email or password")
	r = ada.post("/auth/login?next=%2Forder%2Fshow%2Fid%2F1", "/auth/login",
		url.Values{"email": {"ADA@example.com"}, "password": {"correct horse"}, "next": {"/order/show/id/1"}})
	expect(t, r, 303)
	if r.location != "/order/show/id/1" {
		t.Fatalf("next redirect = %q", r.location)
	}
	expect(t, ada.do("GET", r.location, nil), 200, "Welcome back", "Keyboard")

	// open redirect attempt via ?next= is neutralised
	expect(t, ada.post("/order", "/auth/logout", url.Values{}), 303)
	r = ada.post("/auth/login", "/auth/login", url.Values{"email": {"ada@example.com"}, "password": {"correct horse"}, "next": {"//evil.example/phish"}})
	if r.location != "/order" {
		t.Fatalf("open redirect: %q", r.location)
	}

	// delete with a token works
	expect(t, ada.post("/order/show/id/1", "/order/delete/id/1", url.Values{}), 303)
	expect(t, ada.do("GET", "/order/show/id/1", nil), 404)
}

func TestPublicPagesStillWork(t *testing.T) {
	cl := newClient(t, testServer(t))
	expect(t, cl.do("GET", "/", nil), 200, "Hello World!", "Sign in")
	expect(t, cl.do("GET", "/hello/name/ouja", nil), 200, "Hello, ouja!")
	expect(t, cl.do("GET", "/user/profile/id/5/tab/posts", nil), 200, "User #5")
	expect(t, cl.do("GET", "/about", nil), 200)
	expect(t, cl.do("GET", "/ping", nil), 200, `"ok":true`)
	expect(t, cl.do("GET", "/css/app.css", nil), 200)
	if u, _ := url.Parse(cl.base); len(cl.c.Jar.Cookies(u)) != 0 {
		t.Fatal("public pages should not create sessions")
	}
}
