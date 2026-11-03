package crayd

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

type itemCtl struct{ Controller }

func (c *itemCtl) Item()       { c.JSON(200, "get "+c.Param("id", "")) }
func (c *itemCtl) PostItem()   { c.JSON(200, "post") }
func (c *itemCtl) PutItem()    { c.JSON(200, "put "+c.Param("name", "")) }
func (c *itemCtl) PatchItem()  { c.JSON(200, "patch") }
func (c *itemCtl) DeleteItem() { c.JSON(200, "delete "+c.Param("id", "")) }
func (c *itemCtl) Show()       { c.JSON(200, "show") } // GET only
func (c *itemCtl) Form()       {}

func methodApp(t *testing.T) *browser {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "item_form.html"), []byte(`{{ $.csrf.Field }}`), 0o644)
	a := New(dir)
	a.Static("")
	a.Sessions(SessionOptions{Store: ServerSessions(NewMemorySessionBackend())})
	a.Register("item", &itemCtl{})
	return newBrowser(t, a)
}

func TestMethodOverride(t *testing.T) {
	b := methodApp(t)
	tok := b.token("/item/form")
	form := func(kv ...string) url.Values {
		v := url.Values{"_csrf": {tok}}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	for _, tc := range []struct {
		method string
		form   url.Values
		code   int
		want   string
	}{
		{"POST", form("_method", "PUT", "name", "pen"), 200, `"put pen"`},
		{"POST", form("_method", "patch"), 200, `"patch"`}, // any case
		{"POST", form("_method", "DELETE"), 200, `"delete 5"`},
		{"POST", form(), 200, `"post"`},                                 // no override: plain POST
		{"POST", form("_method", "GET"), 400, ""},                       // only PUT, PATCH, DELETE
		{"POST", form("_method", "BREW"), 400, ""},                      // unknown
		{"GET", url.Values{"_method": {"DELETE"}}, 200, `"get 5"`},      // never from GET
		{"POST", url.Values{"_method": {"DELETE"}}, 403, ""},            // CSRF still applies
		{"PUT", form("name", "real"), 200, `"put real"`},                // real verbs still work
		{"PUT", form("_method", "DELETE", "name", "x"), 200, `"put x"`}, // only a POST can be overridden
	} {
		w := b.do(tc.method, "/item/item/id/5", tc.form)
		if w.Code != tc.code || (tc.want != "" && !bytes.Contains(w.Body.Bytes(), []byte(tc.want))) {
			t.Errorf("%s %v: %d %q, want %d %s", tc.method, tc.form.Get("_method"), w.Code, w.Body.String(), tc.code, tc.want)
		}
	}

	// _method in the query string is ignored: only the POST body counts
	if w := b.do("POST", "/item/item/id/5?_method=DELETE", form()); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"post"`)) {
		t.Errorf("query _method: %d %q", w.Code, w.Body.String())
	}
	// an override to a verb the action doesn't have is a 405
	if w := b.do("POST", "/item/show", form("_method", "DELETE")); w.Code != 405 {
		t.Errorf("override to missing verb: %d", w.Code)
	}
}

func TestMethodOverrideMultipart(t *testing.T) {
	b := methodApp(t)
	tok := b.token("/item/form")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("_csrf", tok)
	mw.WriteField("_method", "PUT")
	mw.WriteField("name", "upload")
	mw.Close()
	r := httptest.NewRequest("POST", "/item/item/id/5", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.a.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"put upload"`)) {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}
