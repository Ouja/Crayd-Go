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
	tpl   *parsedViews
	stamp string // fingerprint of the files tpl was parsed from
}

// The files in the views directory:
//
//	layout.html          default layout; the view goes where it says {{ template "content" . }}
//	layout_<name>.html   other layouts, picked with c.SetLayout("<name>")
//	_<name>.html         partials, usable everywhere as {{ template "_<name>" . }}
//	<ctrl>_<action>.html views
type parsedViews struct {
	views   map[string]bool
	layouts map[string]bool     // "layout", "layout_admin", ...
	tpl     map[string]compiled // key: layout + "|" + view, layout "" = no layout
}

type compiled struct {
	t    *template.Template
	root string
}

func isLayout(name string) bool { return name == "layout" || strings.HasPrefix(name, "layout_") }

// mustLoadViews parses every *.html in dir once. A template error at startup panics.
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

// parseViews builds every view once without a layout and once inside each layout,
// so a broken file is reported when the views are loaded, not on some later request.
func parseViews(dir string) (*parsedViews, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	src := make(map[string]string, len(files))
	var partials, layouts, views []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		body, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		src[name] = string(body)
		switch {
		case strings.HasPrefix(name, "_"):
			partials = append(partials, name)
		case isLayout(name):
			layouts = append(layouts, name)
		default:
			views = append(views, name)
		}
	}

	base := template.New("")
	for _, name := range partials {
		if _, err := base.New(name).Parse(src[name]); err != nil {
			return nil, fmt.Errorf("%s.html: %w", name, err)
		}
	}

	p := &parsedViews{views: map[string]bool{}, layouts: map[string]bool{}, tpl: map[string]compiled{}}
	for _, v := range views {
		t, err := base.Clone()
		if err == nil {
			_, err = t.New(v).Parse(src[v])
		}
		if err != nil {
			return nil, fmt.Errorf("%s.html: %w", v, err)
		}
		p.views[v] = true
		p.tpl["|"+v] = compiled{t, v}
	}
	for _, l := range layouts {
		lt, err := base.Clone()
		if err == nil {
			_, err = lt.New(l).Parse(src[l])
		}
		if err != nil {
			return nil, fmt.Errorf("%s.html: %w", l, err)
		}
		p.layouts[l] = true
		for _, v := range views {
			t, err := lt.Clone()
			if err == nil {
				_, err = t.New("content").Parse(src[v])
			}
			if err != nil {
				return nil, fmt.Errorf("%s.html in %s.html: %w", v, l, err)
			}
			p.tpl[l+"|"+v] = compiled{t, l}
		}
	}
	return p, nil
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

func (vs *viewSet) current() *parsedViews {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.tpl
}

func (vs *viewSet) has(name string) bool { return vs.current().views[name] }

func (vs *viewSet) names() []string {
	p := vs.current()
	out := make([]string, 0, len(p.views))
	for n := range p.views {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// render executes the controller's view into a buffer first,
// so a template error never sends half a page.
func (vs *viewSet) render(w http.ResponseWriter, c *Controller) {
	p := vs.current()
	layout := "layout"
	switch {
	case !c.layoutSet && !p.layouts["layout"]:
		layout = "" // no layout.html: views are whole pages
	case c.layoutSet && c.layout == "":
		layout = ""
	case c.layoutSet && c.layout != "default":
		layout = "layout_" + c.layout
	}
	if layout != "" && !p.layouts[layout] {
		log.Printf("crayd: layout not found: %s.html", layout)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	t, ok := p.tpl[layout+"|"+c.tpl]
	if !ok {
		log.Printf("crayd: view not found: %s.html", c.tpl)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.t.ExecuteTemplate(&buf, t.root, c.View); err != nil {
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
