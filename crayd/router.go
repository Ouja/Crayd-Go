package crayd

import (
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"unicode"
)

var verbPrefixes = []string{"Get", "Post", "Put", "Patch", "Delete"}

// splitVerb turns "PostLogin" into ("POST", "Login"). No prefix means GET.
func splitVerb(name string) (verb, rest string) {
	for _, p := range verbPrefixes {
		if len(name) > len(p) && strings.HasPrefix(name, p) && unicode.IsUpper(rune(name[len(p)])) {
			return strings.ToUpper(p), name[len(p):]
		}
	}
	return http.MethodGet, name
}

// kebab turns "ShowAll" into "show-all" and "HTMLPage" into "html-page".
func kebab(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 {
			prev := r[i-1]
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('-')
			}
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

// route resolves URL segments the Crayd way.
// It returns the controller, action and how many segments were consumed;
// the remaining segments are /key/value parameter pairs.
func (a *App) route(seg []string) (ctrl, action string, used int, ok bool) {
	if len(seg) == 0 {
		return "index", "index", 0, a.has("index", "index")
	}
	first := strings.ToLower(seg[0])
	if a.controllers[first] != nil || a.views.has(first+"_index") {
		if len(seg) > 1 {
			if second := strings.ToLower(seg[1]); a.has(first, second) {
				return first, second, 2, true
			}
		}
		return first, "index", 1, a.has(first, "index")
	}
	if a.has("index", first) {
		return "index", first, 1, true
	}
	return "", "", 0, false
}

func (a *App) has(ctrl, action string) bool {
	if info := a.controllers[ctrl]; info != nil && info.actions[action] != nil {
		return true
	}
	return a.views.has(ctrl + "_" + action)
}

// dispatch is Crayd's init.php + Controller::dispatch.
func (a *App) dispatch(w http.ResponseWriter, r *http.Request) {
	if a.serveStatic(w, r) {
		return
	}
	if a.views.dev { // development only: the error text goes to the browser
		if err := a.views.refresh(); err != nil {
			log.Printf("crayd: views: %v", err)
			http.Error(w, "Template error (development mode):\n\n"+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var seg []string
	for _, s := range strings.Split(r.URL.Path, "/") {
		if s != "" {
			seg = append(seg, s)
		}
	}
	ctrl, action, used, ok := a.route(seg)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Pick the method for this HTTP verb.
	verb := r.Method
	if verb == http.MethodHead {
		verb = http.MethodGet
	}
	info := a.controllers[ctrl]
	var method *actionMethod
	var allowed []string
	if info != nil {
		for v, m := range info.actions[action] {
			allowed = append(allowed, v)
			if v == verb {
				m := m
				method = &m
			}
		}
	}
	viewOnly := a.views.has(ctrl + "_" + action)
	if viewOnly {
		allowed = append(allowed, http.MethodGet)
	}
	if method == nil && !(viewOnly && verb == http.MethodGet) {
		w.Header().Set("Allow", strings.Join(uniq(allowed), ", "))
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Params: query < /key/value pairs < form body (max 32 MB)
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var perr error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		perr = r.ParseMultipartForm(maxBodyBytes)
	} else {
		perr = r.ParseForm()
	}
	if perr != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	params := url.Values{}
	for k, v := range r.URL.Query() {
		params[k] = v
	}
	for i := used; i+1 < len(seg); i += 2 {
		params.Set(seg[i], seg[i+1])
	}
	for k, v := range r.PostForm {
		params[k] = v
	}

	// Fresh controller per request, copied from the registered prototype.
	var inst reflect.Value
	if info != nil {
		inst = reflect.New(info.proto.Elem().Type())
		inst.Elem().Set(info.proto.Elem())
	} else {
		inst = reflect.ValueOf(&struct{ Controller }{})
	}
	c := inst.Interface().(controller).base()
	*c = Controller{W: w, R: r, View: map[string]any{}, Controller: ctrl, Action: action,
		params: params, tpl: ctrl + "_" + action}

	// Sessions: saved by cw right before the first byte of the response.
	var cw *commitWriter
	if a.sessions != nil {
		c.session = &Session{opts: a.sessions, r: r}
		cw = &commitWriter{ResponseWriter: w, commit: func() { c.session.commit(w) }}
		c.W = cw
	}
	c.View["csrf"] = CSRF{s: c.session}

	if reason := a.checkCSRF(c, r); reason != "" {
		http.Error(c.W, "Forbidden: "+reason, http.StatusForbidden)
		return
	}

	if h, ok := inst.Interface().(PreDispatcher); ok {
		h.PreDispatch()
	}
	if method != nil && !c.done {
		inst.Method(method.index).Call(nil)
	}
	if h, ok := inst.Interface().(PostDispatcher); ok && !c.done {
		h.PostDispatch()
	}
	if !c.done {
		if c.session != nil { // one-time flash message from the previous request
			if f := c.session.Get(flashKey); f != "" {
				c.View["flash"] = f
				c.session.Delete(flashKey)
			}
		}
		a.views.render(c.W, c)
	}
	if cw != nil {
		cw.fire() // the action may not have written anything
	}
}

const maxBodyBytes = 32 << 20

// serveStatic serves files from the public dir. Directories are never listed.
func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	if a.public == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		return false
	}
	st, err := fs.Stat(a.public, name)
	if err != nil || st.IsDir() {
		return false
	}
	http.ServeFileFS(w, r, a.public, name)
	return true
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
