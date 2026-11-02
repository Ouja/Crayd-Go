package controllers

import "time"

type Index struct{ Base }

// GET /
func (c *Index) Index() { c.View["var"] = "World!" }

// GET /hello/name/ouja
func (c *Index) Hello() { c.View["name"] = c.Param("name", "stranger") }

// GET /ping
func (c *Index) Ping() {
	c.JSON(200, map[string]any{"ok": true, "time": time.Now().Format(time.RFC3339)})
}

type User struct{ Base }

// GET /user/profile/id/5/tab/posts
func (c *User) Profile() {
	c.View["id"] = c.ParamInt("id", 0)
	c.View["tab"] = c.Param("tab", "overview")
}
