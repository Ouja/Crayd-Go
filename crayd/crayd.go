// Package crayd is a tiny convention-over-configuration MVC framework,
// a Go version of the Crayd PHP framework.
//
//	/                        -> Index.Index()
//	/hello                   -> Index.Hello()        (falls back to the index controller)
//	/user/profile/id/5       -> User.Profile()       with Param("id") == "5"
//	POST /user/save          -> User.PostSave()      (verb prefix: Get, Post, Put, Patch, Delete)
//	POST + _method=DELETE    -> User.DeleteSave()    (for HTML forms, which only send GET and POST)
//	/user/show-all           -> User.ShowAll()       (CamelCase becomes kebab-case)
//	/about                   -> views/about_index.html (view-only page, no controller)
//	/css/app.css             -> public/css/app.css   (static files win over routes)
package crayd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// App holds controllers, views and middleware. It is an http.Handler.
type App struct {
	views       *viewSet
	controllers map[string]*ctrlInfo
	middleware  []func(http.Handler) http.Handler
	public      fs.FS

	sessions       *SessionOptions
	trustedOrigins map[string]bool
	csrfExempt     map[string]bool

	// ShutdownTimeout is how long running requests get to finish when the app is
	// told to stop (default 15s). After that their connections are closed.
	ShutdownTimeout time.Duration
	onShutdown      []func(context.Context) error

	once    sync.Once
	handler http.Handler
}

type ctrlInfo struct {
	typeName string
	proto    reflect.Value                      // the registered *Struct, copied per request
	actions  map[string]map[string]actionMethod // action -> HTTP verb -> method
}

type actionMethod struct {
	index int
	name  string
}

// New loads layout.html and every view in viewDir, and serves ./public if it exists.
func New(viewDir string) *App {
	a := &App{views: mustLoadViews(viewDir), controllers: map[string]*ctrlInfo{},
		trustedOrigins: map[string]bool{}, csrfExempt: map[string]bool{}}
	a.Static("public")
	return a
}

// Static sets the directory for static files. An empty or missing dir disables it.
func (a *App) Static(dir string) {
	a.public = nil
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		a.public = os.DirFS(dir)
	}
}

// Use adds standard net/http middleware. The first one added runs first.
// Call Use before the app starts serving.
func (a *App) Use(mw ...func(http.Handler) http.Handler) {
	a.middleware = append(a.middleware, mw...)
}

// Register adds a controller under name. The struct you pass is a prototype:
// each request gets a fresh copy of it, so services set on its fields
// (DB, cache, ...) are shared while View and params are per request.
//
// Actions are exported methods with no arguments and no results.
func (a *App) Register(name string, c controller) {
	v := reflect.ValueOf(c)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		panic("crayd: Register needs a pointer to a struct, got " + v.Type().String())
	}
	t := v.Type()
	info := &ctrlInfo{typeName: t.Elem().Name(), proto: v, actions: map[string]map[string]actionMethod{}}
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if m.Type.NumIn() != 1 || m.Type.NumOut() != 0 || isFrameworkMethod(m) ||
			m.Name == "PreDispatch" || m.Name == "PostDispatch" {
			continue
		}
		verb, rest := splitVerb(m.Name)
		action := kebab(rest)
		if info.actions[action] == nil {
			info.actions[action] = map[string]actionMethod{}
		}
		info.actions[action][verb] = actionMethod{index: i, name: m.Name}
	}
	a.controllers[strings.ToLower(name)] = info
}

// isFrameworkMethod reports whether m is promoted from the embedded Controller
// (same name and signature), so framework helpers never become URL actions.
func isFrameworkMethod(m reflect.Method) bool {
	bm, ok := reflect.TypeOf(&Controller{}).MethodByName(m.Name)
	if !ok || bm.Type.NumIn() != m.Type.NumIn() || bm.Type.NumOut() != m.Type.NumOut() {
		return false
	}
	for i := 1; i < m.Type.NumIn(); i++ {
		if bm.Type.In(i) != m.Type.In(i) {
			return false
		}
	}
	return true
}

// ServeHTTP runs the middleware chain and the dispatcher, recovering from panics.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.once.Do(func() {
		var h http.Handler = http.HandlerFunc(a.dispatch)
		for i := len(a.middleware) - 1; i >= 0; i-- {
			h = a.middleware[i](h)
		}
		a.handler = h
	})
	defer func() {
		if err := recover(); err != nil {
			if err == http.ErrAbortHandler {
				panic(err)
			}
			log.Printf("crayd: panic serving %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()
	a.handler.ServeHTTP(w, r)
}

// Routes lists every reachable route, for printing or debugging.
func (a *App) Routes() []string {
	var out []string
	pathFor := func(ctrl, action string) string {
		switch {
		case ctrl == "index" && action == "index":
			return "/"
		case action == "index":
			return "/" + ctrl
		case ctrl == "index":
			return "/" + action
		}
		return "/" + ctrl + "/" + action
	}
	for name, info := range a.controllers {
		for action, verbs := range info.actions {
			for verb, m := range verbs {
				out = append(out, pad(verb, 7)+pad(pathFor(name, action), 28)+info.typeName+"."+m.name+"()")
			}
		}
	}
	for _, v := range a.views.names() {
		ctrl, action, _ := strings.Cut(v, "_")
		if info := a.controllers[ctrl]; info != nil && info.actions[action] != nil {
			continue
		}
		out = append(out, pad("GET", 7)+pad(pathFor(ctrl, action), 28)+"(view only: "+v+".html)")
	}
	sort.Slice(out, func(i, j int) bool { return out[i][7:] < out[j][7:] || (out[i][7:] == out[j][7:] && out[i] < out[j]) })
	return out
}

// Dev switches development mode: view files are re-read when they change, and a
// template error is shown in the browser. Never enable it in production.
// Call it before serving.
func (a *App) Dev(on bool) { a.views.dev = on }

// OnShutdown registers cleanup (close the database, flush Redis, ...). Hooks run
// after the server has stopped taking and finishing requests, in reverse order of
// registration, so what was opened last is closed first.
func (a *App) OnShutdown(fn func(ctx context.Context) error) {
	a.onShutdown = append(a.onShutdown, fn)
}

// Run prints the route table, serves on addr and stops cleanly on Ctrl+C or SIGTERM
// (see RunContext). A second Ctrl+C while shutting down quits immediately.
func (a *App) Run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); stop() }() // restore default handling: a second signal kills the process
	return a.RunContext(ctx, addr)
}

// RunContext is Run with your own context: the server stops when ctx is cancelled.
func (a *App) RunContext(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.runShutdownHooks()
		return err
	}
	log.Println("crayd routes:")
	for _, r := range a.Routes() {
		log.Println("  " + r)
	}
	if a.views.dev {
		log.Println("crayd: development mode (views reload when their files change)")
	}
	log.Println("crayd listening on", ln.Addr())
	return a.Serve(ctx, ln)
}

// Serve serves on ln until ctx is cancelled or the listener fails. On cancel it stops
// accepting connections, lets running requests finish (up to ShutdownTimeout), then runs
// the OnShutdown hooks. It returns nil after a clean shutdown.
//
// Running requests keep a live context: cancelling ctx does not cancel their c.Ctx(),
// so database queries in progress can complete.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	var err error
	select {
	case err = <-serveErr: // the listener failed
	case <-ctx.Done():
		log.Println("crayd: shutting down, waiting for running requests...")
		timeout := a.ShutdownTimeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		sctx, cancel := context.WithTimeout(context.Background(), timeout)
		if shutErr := srv.Shutdown(sctx); shutErr != nil {
			srv.Close() // too slow: cut the remaining connections
			err = fmt.Errorf("crayd: shutdown: %w (connections were closed)", shutErr)
		}
		cancel()
		<-serveErr // Serve returns http.ErrServerClosed
	}
	if hookErr := a.runShutdownHooks(); hookErr != nil {
		err = errors.Join(err, hookErr)
	}
	return err
}

func (a *App) runShutdownHooks() error {
	timeout := a.ShutdownTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error
	for i := len(a.onShutdown) - 1; i >= 0; i-- {
		if err := a.onShutdown[i](ctx); err != nil {
			errs = append(errs, err) // keep going: the other hooks still need to run
		}
	}
	a.onShutdown = nil
	return errors.Join(errs...)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}
