package crayd

import (
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type devCtl struct{ Controller }

func (c *devCtl) Index() { c.View["who"] = "world" }

// devApp serves views from a temp dir we can edit while the app runs.
type devApp struct {
	t    *testing.T
	dir  string
	app  *App
	tick int
}

func newDevApp(t *testing.T, dev bool) *devApp {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	d := &devApp{t: t, dir: t.TempDir()}
	d.write("layout.html", `[{{ template "content" . }}]`)
	d.write("index_index.html", `v1 {{ .who }}`)
	d.app = New(d.dir)
	d.app.Static("")
	d.app.Register("index", &devCtl{})
	d.app.Dev(dev)
	return d
}

// write saves a view and gives it a new modification time, so the change is seen
// even on file systems with coarse timestamps.
func (d *devApp) write(name, content string) {
	d.t.Helper()
	p := filepath.Join(d.dir, name)
	tmp := p + ".tmp" // not *.html, so the app ignores it; rename = atomic save, like most editors
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		d.t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		d.t.Fatal(err)
	}
	d.tick++
	mt := time.Now().Add(time.Duration(d.tick) * time.Hour)
	os.Chtimes(p, mt, mt)
}

func (d *devApp) get(path string) (int, string) {
	w := httptest.NewRecorder()
	d.app.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Code, w.Body.String()
}

func TestDevReloadsChangedViews(t *testing.T) {
	d := newDevApp(t, true)
	if code, body := d.get("/"); code != 200 || body != "[v1 world]" {
		t.Fatalf("%d %q", code, body)
	}
	d.write("index_index.html", `v2 {{ .who }}!`)
	if _, body := d.get("/"); body != "[v2 world!]" {
		t.Fatalf("edit not picked up: %q", body)
	}
	d.write("layout.html", `(({{ template "content" . }}))`)
	if _, body := d.get("/"); body != "((v2 world!))" {
		t.Fatalf("layout edit not picked up: %q", body)
	}
}

func TestDevPicksUpNewAndRemovedViews(t *testing.T) {
	d := newDevApp(t, true)
	if code, _ := d.get("/about"); code != 404 {
		t.Fatalf("before: %d", code)
	}
	d.write("about_index.html", `about page`)
	if code, body := d.get("/about"); code != 200 || body != "[about page]" {
		t.Fatalf("new view-only page: %d %q", code, body)
	}
	os.Remove(filepath.Join(d.dir, "about_index.html"))
	if code, _ := d.get("/about"); code != 404 {
		t.Fatalf("removed view still served: %d", code)
	}
}

func TestDevShowsTemplateErrorsAndRecovers(t *testing.T) {
	d := newDevApp(t, true)
	d.get("/")
	d.write("index_index.html", `broken {{ .who `)
	code, body := d.get("/")
	if code != 500 || !strings.Contains(body, "Template error (development mode)") || !strings.Contains(body, "index_index.html") {
		t.Fatalf("%d %q", code, body)
	}
	if code2, _ := d.get("/"); code2 != 500 {
		t.Fatal("must keep failing until the file is fixed")
	}
	d.write("index_index.html", `fixed {{ .who }}`)
	if code, body := d.get("/"); code != 200 || body != "[fixed world]" {
		t.Fatalf("after fixing: %d %q", code, body)
	}
}

func TestProductionNeverReloadsAndNeverShowsErrors(t *testing.T) {
	d := newDevApp(t, false)
	d.write("index_index.html", `changed`)
	if _, body := d.get("/"); body != "[v1 world]" {
		t.Fatalf("production reloaded a view: %q", body)
	}
	d.write("index_index.html", `broken {{ `)
	if code, body := d.get("/"); code != 200 || body != "[v1 world]" {
		t.Fatalf("production must keep serving the parsed templates: %d %q", code, body)
	}
}

func TestStartupTemplateErrorPanics(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index_index.html"), []byte(`{{ oops`), 0o644)
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "index_index.html") {
			t.Fatalf("recover = %v", r)
		}
	}()
	New(dir)
}

func TestDevReloadIsRaceFree(t *testing.T) {
	d := newDevApp(t, true)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if code, body := d.get("/"); code != 200 || !strings.HasPrefix(body, "[v") {
						t.Errorf("%d %q", code, body)
						return
					}
				}
			}
		}()
	}
	for i := 0; i < 20; i++ { // keep rewriting while requests run
		d.write("index_index.html", `v`+string(rune('a'+i))+` {{ .who }}`)
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}
