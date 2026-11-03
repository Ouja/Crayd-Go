package crayd

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type hdrCtl struct{ Controller }

func (c *hdrCtl) Index() { c.View["msg"] = "page" }
func (c *hdrCtl) Widget() {
	c.W.Header().Del("X-Frame-Options")
	c.W.Header().Del("Content-Security-Policy")
	c.JSON(200, "embeddable")
}
func (c *hdrCtl) Boom() { panic("boom") }

func headerApp(t *testing.T) *App {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index_index.html"), []byte(`{{ .msg }}`), 0o644)
	pub := filepath.Join(dir, "public")
	os.MkdirAll(pub, 0o755)
	os.WriteFile(filepath.Join(pub, "app.css"), []byte(`body{}`), 0o644)
	a := New(dir)
	a.Static(pub)
	a.Register("index", &hdrCtl{})
	return a
}

func get(a *App, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestDefaultSecurityHeaders(t *testing.T) {
	quietLogs(t)
	a := headerApp(t)
	for _, path := range []string{"/", "/app.css", "/no-such-page", "/boom"} {
		w := get(a, path)
		for k, v := range DefaultHeaders() {
			if got := w.Header().Get(k); got != v[0] {
				t.Errorf("%s (%d): %s = %q, want %q", path, w.Code, k, got, v[0])
			}
		}
	}
}

func TestSecurityHeadersCanBeChanged(t *testing.T) {
	a := headerApp(t)
	a.Headers.Set("X-Frame-Options", "DENY")
	a.Headers.Set("Strict-Transport-Security", "max-age=31536000")
	a.Headers.Del("Referrer-Policy")
	w := get(a, "/")
	if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Strict-Transport-Security") != "max-age=31536000" ||
		w.Header().Get("Referrer-Policy") != "" {
		t.Fatalf("app-wide change not applied: %v", w.Header())
	}

	// one action can opt out for its own response
	w = get(a, "/widget")
	if w.Header().Get("X-Frame-Options") != "" || w.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("action could not remove headers: %v", w.Header())
	}
	if get(a, "/").Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("an action's change leaked into the app-wide headers")
	}

	a.Headers = nil // no headers at all is allowed
	if w := get(a, "/"); w.Code != http.StatusOK || w.Header().Get("X-Content-Type-Options") != "" {
		t.Fatalf("nil Headers: %d %v", w.Code, w.Header())
	}
}

func quietLogs(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
}
