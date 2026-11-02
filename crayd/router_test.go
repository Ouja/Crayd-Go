package crayd

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type greeter struct{ prefix string }

type tIndex struct {
	Controller
	G *greeter // injected service
}

func (c *tIndex) Index()       { c.View["msg"] = "home" }
func (c *tIndex) Hello()       { c.View["msg"] = c.G.prefix + c.Param("name", "stranger") }
func (c *tIndex) PostHello()   { c.Redirect("/hello/name/" + c.Param("name", "")) }
func (c *tIndex) ShowAll()     { c.View["msg"] = "all" }
func (c *tIndex) Boom()        { panic("kaboom") }
func (c *tIndex) Ping()        { c.JSON(200, map[string]any{"ok": true, "n": c.ParamInt("n", 0)}) }
func (c *tIndex) Missing()     { c.NotFound() }
func (c *tIndex) helper()      {} // unexported: never an action
func (c *tIndex) WithArg(int)  {} // has args: never an action
func (c *tIndex) PreDispatch() { c.View["pre"] = "yes" }
func (c *tIndex) DeleteThing() { c.JSON(200, "deleted") }
func (c *tIndex) Postcard()    { c.View["msg"] = "postcard is GET" }
func (c *tIndex) Mutate()      { c.G = &greeter{prefix: "mutated "} } // must not leak to other requests
func (c *tIndex) SetDirty()    { c.View["msg"] = "x"; c.SetStatus(418) }
func (c *tIndex) BadView()     { c.SetView("nope") }

type tUser struct{ Controller }

func (c *tUser) Index() { c.View["msg"] = "user index id=" + c.Param("id", "-") }
func (c *tUser) Profile() {
	c.View["msg"] = "profile " + c.Param("id", "?") + " " + c.Param("tab", "overview")
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	views := filepath.Join(dir, "views")
	pub := filepath.Join(dir, "public")
	os.MkdirAll(views, 0o755)
	os.MkdirAll(filepath.Join(pub, "css"), 0o755)
	write := func(p, s string) { os.WriteFile(p, []byte(s), 0o644) }
	write(filepath.Join(views, "layout.html"), `[{{ template "content" . }}]`)
	for _, v := range []string{"index_index", "index_hello", "index_show-all", "user_index", "user_profile", "index_set-dirty"} {
		write(filepath.Join(views, v+".html"), `{{ .msg }}{{ with .pre }}|{{ . }}{{ end }}`)
	}
	write(filepath.Join(views, "index_postcard.html"), `{{ .msg }}`)
	write(filepath.Join(views, "about_index.html"), `about page`)
	write(filepath.Join(views, "index_terms.html"), `terms page`)
	write(filepath.Join(pub, "css", "app.css"), `body{}`)

	a := New(views)
	a.Static(pub)
	a.Register("index", &tIndex{G: &greeter{prefix: "Hi "}})
	a.Register("user", &tUser{})
	return a
}

func do(a *App, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestRouting(t *testing.T) {
	log.SetOutput(&bytes.Buffer{}) // keep panic logs quiet
	defer log.SetOutput(os.Stderr)
	a := newTestApp(t)

	tests := []struct {
		name, method, path, body string
		code                     int
		want                     string
	}{
		{"root", "GET", "/", "", 200, "[home|yes]"},
		{"index fallback", "GET", "/hello", "", 200, "[Hi stranger|yes]"},
		{"explicit index ctrl", "GET", "/index/hello/name/ouja", "", 200, "[Hi ouja|yes]"},
		{"query param", "GET", "/hello?name=bob", "", 200, "[Hi bob|yes]"},
		{"route pair beats query", "GET", "/hello/name/a?name=b", "", 200, "Hi a"},
		{"escaped output", "GET", "/hello?name=%3Cb%3E", "", 200, "Hi &lt;b&gt;"},
		{"case-insensitive", "GET", "/HELLO", "", 200, "Hi stranger"},
		{"kebab action", "GET", "/show-all", "", 200, "[all|yes]"},
		{"controller action params", "GET", "/user/profile/id/5/tab/posts", "", 200, "[profile 5 posts]"},
		{"controller index", "GET", "/user", "", 200, "user index id=-"},
		{"unknown action -> ctrl index w/ params", "GET", "/user/id/7", "", 200, "user index id=7"},
		{"odd trailing segment ignored", "GET", "/user/profile/id", "", 200, "profile ?"},
		{"view-only controller", "GET", "/about", "", 200, "[about page]"},
		{"view-only index action", "GET", "/terms", "", 200, "[terms page]"},
		{"not found", "GET", "/nope", "", 404, ""},
		{"traversal", "GET", "/../etc/passwd", "", 404, ""},
		{"framework method not routable", "GET", "/redirect", "", 404, ""},
		{"unexported not routable", "GET", "/helper", "", 404, ""},
		{"method with args not routable", "GET", "/with-arg", "", 404, ""},
		{"prefixed name is not a verb", "GET", "/postcard", "", 200, "postcard is GET"},
		{"POST action", "POST", "/hello", "name=zed", 303, ""},
		{"DELETE action", "DELETE", "/thing", "", 200, `"deleted"`},
		{"GET on DELETE-only", "GET", "/thing", "", 405, ""},
		{"POST on GET-only", "POST", "/show-all", "", 405, ""},
		{"POST on view-only", "POST", "/about", "", 405, ""},
		{"HEAD works like GET", "HEAD", "/hello", "", 200, ""},
		{"json", "GET", "/ping/n/3", "", 200, `{"n":3,"ok":true}`},
		{"controller NotFound", "GET", "/missing", "", 404, ""},
		{"SetStatus", "GET", "/set-dirty", "", 418, "[x|yes]"},
		{"missing view", "GET", "/bad-view", "", 500, ""},
		{"panic recovered", "GET", "/boom", "", 500, "Internal Server Error"},
		{"static file", "GET", "/css/app.css", "", 200, "body{}"},
		{"static dir not listed", "GET", "/css", "", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(a, tt.method, tt.path, tt.body)
			if w.Code != tt.code {
				t.Fatalf("%s %s: code %d, want %d (body %q)", tt.method, tt.path, w.Code, tt.code, w.Body.String())
			}
			if tt.want != "" && !strings.Contains(w.Body.String(), tt.want) {
				t.Fatalf("%s %s: body %q, want it to contain %q", tt.method, tt.path, w.Body.String(), tt.want)
			}
		})
	}
	if got := do(a, "DELETE", "/show-all", "").Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow header = %q, want GET", got)
	}
}

func TestPrototypeIsolation(t *testing.T) {
	a := newTestApp(t)
	do(a, "GET", "/mutate", "") // one request swaps its own copy's service
	if w := do(a, "GET", "/hello", ""); !strings.Contains(w.Body.String(), "Hi stranger") {
		t.Fatalf("service leaked between requests: %q", w.Body.String())
	}
}

func TestMiddlewareOrder(t *testing.T) {
	a := newTestApp(t)
	var order []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	a.Use(mw("first"), mw("second"))
	do(a, "GET", "/", "")
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("order = %v", order)
	}
}

func TestRouteTable(t *testing.T) {
	routes := strings.Join(newTestApp(t).Routes(), "\n")
	for _, want := range []string{"/hello", "tIndex.PostHello()", "DELETE", "/thing", "(view only: about_index.html)", "/user/profile"} {
		if !strings.Contains(routes, want) {
			t.Errorf("route table missing %q:\n%s", want, routes)
		}
	}
	for _, bad := range []string{"Redirect", "helper", "WithArg", "PreDispatch"} {
		if strings.Contains(routes, bad) {
			t.Errorf("route table should not contain %q", bad)
		}
	}
}

func TestKebabAndVerb(t *testing.T) {
	for in, want := range map[string]string{"Index": "index", "ShowAll": "show-all", "HTMLPage": "html-page", "Page2Edit": "page2-edit"} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"PostLogin": "POST Login", "Postcard": "GET Postcard", "Post": "GET Post", "DeleteItem": "DELETE Item"} {
		v, r := splitVerb(in)
		if got := v + " " + r; got != want {
			t.Errorf("splitVerb(%q) = %q, want %q", in, got, want)
		}
	}
}
