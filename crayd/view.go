package crayd

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// viewSet holds the parsed templates. In development mode it re-parses them when
// a file in the views directory is added, removed or changed.
type viewSet struct {
	dir string
	dev bool // set before serving, see App.Dev

	mu    sync.RWMutex
	tpl   map[string]*template.Template
	stamp string // fingerprint of the files tpl was parsed from
}

// mustLoadViews parses every *.html in dir once. If layout.html exists, each view is
// rendered inside it as {{ template "content" . }}. A template error at startup panics.
func mustLoadViews(dir string) *viewSet {
	vs := &viewSet{dir: dir}
	stamp := vs.fingerprint() // taken before parsing, so an edit during the parse is noticed later
	tpl, err := parseViews(dir)
	if err != nil {
		panic("crayd: " + err.Error())
	}
	vs.tpl, vs.stamp = tpl, stamp
	return vs
}

func parseViews(dir string) (map[string]*template.Template, error) {
	var layout *template.Template
	if p := filepath.Join(dir, "layout.html"); fileExists(p) {
		var err error
		if layout, err = template.ParseFiles(p); err != nil {
			return nil, err
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	out := make(map[string]*template.Template, len(files))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		if name == "layout" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t *template.Template
		if layout != nil {
			if t, err = layout.Clone(); err == nil {
				_, err = t.New("content").Parse(string(body))
			}
		} else {
			t, err = template.New(name).Parse(string(body))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		out[name] = t
	}
	return out, nil
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// fingerprint changes whenever a view file is added, removed or modified.
func (vs *viewSet) fingerprint() string {
	files, _ := filepath.Glob(filepath.Join(vs.dir, "*.html"))
	var b strings.Builder
	for _, f := range files { // Glob returns them sorted
		if st, err := os.Stat(f); err == nil {
			b.WriteString(filepath.Base(f))
			b.WriteByte('|')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
			b.WriteByte('|')
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// refresh re-parses the templates if their files changed. On a template error the
// previous templates stay in use and the error is returned (and returned again on
// every request until the file is fixed).
func (vs *viewSet) refresh() error {
	stamp := vs.fingerprint()
	vs.mu.RLock()
	same := stamp == vs.stamp
	vs.mu.RUnlock()
	if same {
		return nil
	}
	tpl, err := parseViews(vs.dir)
	if err != nil {
		return err
	}
	vs.mu.Lock()
	vs.tpl, vs.stamp = tpl, stamp
	vs.mu.Unlock()
	log.Println("crayd: views reloaded")
	return nil
}

func (vs *viewSet) get(name string) (*template.Template, bool) {
	vs.mu.RLock()
	t, ok := vs.tpl[name]
	vs.mu.RUnlock()
	return t, ok
}

func (vs *viewSet) has(name string) bool { _, ok := vs.get(name); return ok }

func (vs *viewSet) names() []string {
	vs.mu.RLock()
	out := make([]string, 0, len(vs.tpl))
	for n := range vs.tpl {
		out = append(out, n)
	}
	vs.mu.RUnlock()
	sort.Strings(out)
	return out
}

// render executes the controller's view into a buffer first,
// so a template error never sends half a page.
func (vs *viewSet) render(w http.ResponseWriter, c *Controller) {
	t, ok := vs.get(c.tpl)
	if !ok {
		log.Printf("crayd: view not found: %s.html", c.tpl)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, c.View); err != nil {
		log.Printf("crayd: render %s: %v", c.tpl, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if c.status != 0 {
		w.WriteHeader(c.status)
	}
	buf.WriteTo(w)
}
