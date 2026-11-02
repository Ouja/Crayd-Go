package crayd

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"
)

// SessionStore turns session data into a cookie value and back.
// Use CookieSessions (data encrypted in the cookie) or ServerSessions
// (random ID in the cookie, data in Redis/memory).
type SessionStore interface {
	// Load returns the data for a cookie value, or nil if it is unknown,
	// tampered with or expired.
	Load(ctx context.Context, cookie string) (map[string]string, error)
	// Save stores data and returns the cookie value to send. old is the current
	// cookie value of an existing session, or "" to start a new one.
	Save(ctx context.Context, old string, data map[string]string, ttl time.Duration) (string, error)
	// Destroy invalidates a session.
	Destroy(ctx context.Context, cookie string) error
}

type SessionOptions struct {
	Store      SessionStore
	CookieName string        // default "crayd_session"
	TTL        time.Duration // idle timeout, default 12h
	// Insecure sends the cookie over plain HTTP too. Only for testing on a local
	// network; http://localhost works without it in modern browsers.
	Insecure bool
}

// Sessions enables sessions, flash messages, login helpers and CSRF tokens.
func (a *App) Sessions(o SessionOptions) {
	if o.Store == nil {
		panic("crayd: SessionOptions.Store is required")
	}
	if o.CookieName == "" {
		o.CookieName = "crayd_session"
	}
	if o.TTL <= 0 {
		o.TTL = 12 * time.Hour
	}
	a.sessions = &o
}

// Session is per request and loads lazily: pages that never touch it cost nothing
// and create no cookie. Keys starting with "_" are reserved for the framework.
type Session struct {
	opts *SessionOptions
	r    *http.Request

	loaded  bool
	existed bool   // a valid session came in with the request
	cookie  string // its cookie value (only set when existed)
	data    map[string]string
	dirty   bool
	rotate  bool
}

const refreshKey = "_r"

func (s *Session) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = map[string]string{}
	ck, err := s.r.Cookie(s.opts.CookieName)
	if err != nil || ck.Value == "" {
		return
	}
	data, err := s.opts.Store.Load(s.r.Context(), ck.Value)
	if err != nil {
		log.Printf("crayd: session load: %v", err)
		return
	}
	if data != nil {
		s.data, s.existed, s.cookie = data, true, ck.Value
	}
}

func (s *Session) Get(key string) string { s.load(); return s.data[key] }

func (s *Session) Set(key, value string) {
	s.load()
	if old, ok := s.data[key]; !ok || old != value {
		s.data[key] = value
		s.dirty = true
	}
}

func (s *Session) Delete(key string) {
	s.load()
	if _, ok := s.data[key]; ok {
		delete(s.data, key)
		s.dirty = true
	}
}

// RenewID gives the session a new ID/cookie, keeping its data.
// Always do this when privileges change (login), to stop session fixation.
func (s *Session) RenewID() { s.load(); s.rotate = true }

// Clear drops all data and the old session ID.
func (s *Session) Clear() {
	s.load()
	s.data = map[string]string{}
	s.dirty, s.rotate = true, true
}

func (s *Session) empty() bool {
	for k := range s.data {
		if k != refreshKey {
			return false
		}
	}
	return true
}

// commit runs just before the response headers are written.
func (s *Session) commit(w http.ResponseWriter) {
	if !s.loaded {
		return
	}
	o, ctx, old := s.opts, s.r.Context(), s.cookie
	if s.rotate && s.existed {
		if err := o.Store.Destroy(ctx, old); err != nil {
			log.Printf("crayd: session destroy: %v", err)
		}
		old = ""
	}
	if s.empty() {
		if s.existed {
			if !s.rotate {
				o.Store.Destroy(ctx, old)
			}
			http.SetCookie(w, s.httpCookie("", -1))
		}
		return
	}
	last, _ := strconv.ParseInt(s.data[refreshKey], 10, 64)
	refresh := s.existed && time.Since(time.Unix(last, 0)) > o.TTL/2 // keep idle timeout sliding
	if !s.dirty && !s.rotate && !refresh {
		return
	}
	s.data[refreshKey] = strconv.FormatInt(time.Now().Unix(), 10)
	val, err := o.Store.Save(ctx, old, s.data, o.TTL)
	if err != nil {
		log.Printf("crayd: session save: %v", err)
		return
	}
	http.SetCookie(w, s.httpCookie(val, 0))
}

func (s *Session) httpCookie(val string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: s.opts.CookieName, Value: val, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: !s.opts.Insecure, SameSite: http.SameSiteLaxMode,
	}
}

// commitWriter saves the session right before the first byte of the response,
// whether the action renders, redirects, writes JSON or writes directly.
type commitWriter struct {
	http.ResponseWriter
	commit func()
	done   bool
}

func (w *commitWriter) fire() {
	if !w.done {
		w.done = true
		w.commit()
	}
}
func (w *commitWriter) WriteHeader(code int)        { w.fire(); w.ResponseWriter.WriteHeader(code) }
func (w *commitWriter) Write(b []byte) (int, error) { w.fire(); return w.ResponseWriter.Write(b) }
func (w *commitWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Session returns the request's session. It panics if app.Sessions was not called.
func (c *Controller) Session() *Session {
	if c.session == nil {
		panic("crayd: sessions are not enabled; call app.Sessions(...)")
	}
	return c.session
}

// Flash stores a one-time message, shown as {{ .flash }} on the next rendered page.
func (c *Controller) Flash(msg string) { c.Session().Set(flashKey, msg) }

const flashKey = "_flash"
