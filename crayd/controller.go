package crayd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// Controller is embedded in every app controller.
type Controller struct {
	W          http.ResponseWriter
	R          *http.Request
	View       map[string]any // data for the template
	Controller string         // resolved controller name
	Action     string         // resolved action name

	params  url.Values
	session *Session
	tpl     string
	status  int
	done    bool
}

type controller interface{ base() *Controller }

func (c *Controller) base() *Controller { return c }

// PreDispatcher and PostDispatcher are optional hooks, like Crayd's preDispatch/postDispatch.
type PreDispatcher interface{ PreDispatch() }
type PostDispatcher interface{ PostDispatch() }

// Ctx is the request context; pass it to database and cache calls.
func (c *Controller) Ctx() context.Context { return c.R.Context() }

// Param returns a form, /key/value or query value, or def if empty.
func (c *Controller) Param(key, def string) string {
	if v := c.params.Get(key); v != "" {
		return v
	}
	return def
}

// ParamInt returns the param as int, or def if missing or invalid.
func (c *Controller) ParamInt(key string, def int) int {
	if n, err := strconv.Atoi(c.params.Get(key)); err == nil {
		return n
	}
	return def
}

// ParamInt64 returns the param as int64, or def if missing or invalid.
func (c *Controller) ParamInt64(key string, def int64) int64 {
	if n, err := strconv.ParseInt(c.params.Get(key), 10, 64); err == nil {
		return n
	}
	return def
}

// Params returns all parameters.
func (c *Controller) Params() url.Values { return c.params }

func (c *Controller) IsPost() bool { return c.R.Method == http.MethodPost }

// SetView overrides the template to render (default: controller_action).
func (c *Controller) SetView(name string) { c.tpl = name }

// SetStatus sets the HTTP status used when the view is rendered.
func (c *Controller) SetStatus(code int) { c.status = code }

// Redirect sends the user elsewhere and skips rendering.
func (c *Controller) Redirect(to string) {
	http.Redirect(c.W, c.R, to, http.StatusSeeOther)
	c.done = true
}

// JSON writes v as JSON and skips rendering.
func (c *Controller) JSON(status int, v any) {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	json.NewEncoder(c.W).Encode(v)
	c.done = true
}

// NotFound sends a 404 and skips rendering.
func (c *Controller) NotFound() {
	http.NotFound(c.W, c.R)
	c.done = true
}
