package controllers

import (
	"errors"
	"strconv"

	"crayd-go/crayd"
	"crayd-go/models"
)

// Auth handles /auth/login, /auth/register and /auth/logout.
type Auth struct {
	Base
	Users *models.UserService
}

// GET /auth/login?next=/order
func (c *Auth) Login() { c.View["next"] = c.Param("next", "") }

// POST /auth/login
func (c *Auth) PostLogin() {
	email, next := c.Param("email", ""), c.Param("next", "")
	u, err := c.Users.Authenticate(c.Ctx(), email, c.Param("password", ""))
	if errors.Is(err, models.ErrBadCredentials) {
		c.View["error"] = "Wrong email or password"
		c.View["form"] = map[string]string{"email": email}
		c.View["next"] = next
		c.SetView("auth_login")
		c.SetStatus(401)
		return
	}
	if err != nil {
		panic(err)
	}
	c.signIn(u)
	c.Flash("Welcome back, " + u.Email)
	c.Redirect(crayd.SafeRedirect(next, "/order"))
}

// GET /auth/register
func (c *Auth) Register() {}

// POST /auth/register
func (c *Auth) PostRegister() {
	email := c.Param("email", "")
	u, err := c.Users.Register(c.Ctx(), email, c.Param("password", ""))
	var verr models.ValidationError
	if errors.As(err, &verr) {
		c.View["errors"] = map[string]string(verr)
		c.View["form"] = map[string]string{"email": email}
		c.SetView("auth_register")
		c.SetStatus(422)
		return
	}
	if err != nil {
		panic(err)
	}
	c.signIn(u)
	c.Flash("Welcome, " + u.Email)
	c.Redirect("/order")
}

// POST /auth/logout
func (c *Auth) PostLogout() {
	c.LogoutUser()
	c.Flash("You are signed out")
	c.Redirect("/")
}

func (c *Auth) signIn(u *models.User) {
	c.LoginAs(strconv.Itoa(u.ID))
	c.Session().Set("email", u.Email)
}
