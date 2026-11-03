// Package controllers holds thin controllers: read params, call a model, pick a view.
// Business rules live in models; SQL and Redis live in store.
package controllers

import "crayd-go/crayd"

// Base is embedded by every controller so shared setup lives in one place.
type Base struct{ crayd.Controller }

func (c *Base) PreDispatch() {
	c.View["site"] = "Crayd.go"
	c.View["controller"] = c.Controller.Controller // the layout highlights the current menu item
	c.View["errors"] = map[string]string{}         // templates can always read .errors.field
	c.View["form"] = map[string]string{}
	if c.UserID() != "" {
		c.View["email"] = c.Session().Get("email")
	}
}
