package controllers

import (
	"errors"
	"fmt"
	"strings"

	"crayd-go/models"
)

// Order is the example CRUD controller. Orders is injected in main.go.
type Order struct {
	Base
	Orders *models.OrderService
}

const formRows = 3

// Every order page needs a signed-in user.
func (c *Order) PreDispatch() {
	c.Base.PreDispatch()
	c.RequireLogin("/auth/login")
}

// GET /order
func (c *Order) Index() {
	orders, err := c.Orders.List(c.Ctx())
	if err != nil {
		panic(err) // recovered by the framework: logged, user sees a 500
	}
	c.View["orders"] = orders
}

// GET /order/new
func (c *Order) New() {
	c.View["rows"] = []int{1, 2, 3}
	c.View["form"] = map[string]string{"customer": c.Session().Get("email")}
}

// POST /order/checkout
func (c *Order) PostCheckout() {
	customer := c.Param("customer", "")
	items := c.itemsFromForm()

	o, err := c.Orders.Checkout(c.Ctx(), customer, items)
	var verr models.ValidationError
	switch {
	case errors.As(err, &verr):
		form := map[string]string{"customer": customer}
		for k, v := range c.Params() {
			form[k] = v[0]
		}
		c.View["errors"], c.View["form"], c.View["rows"] = map[string]string(verr), form, []int{1, 2, 3}
		c.SetView("order_new")
		c.SetStatus(422)
	case err != nil:
		panic(err)
	default:
		c.Redirect(fmt.Sprintf("/order/show/id/%d", o.ID))
	}
}

// GET /order/show/id/5
func (c *Order) Show() {
	o, err := c.Orders.Get(c.Ctx(), c.ParamInt("id", 0))
	if errors.Is(err, models.ErrNotFound) {
		c.NotFound()
		return
	}
	if err != nil {
		panic(err)
	}
	c.View["order"] = o
}

// POST /order/delete/id/5
func (c *Order) PostDelete() {
	err := c.Orders.Delete(c.Ctx(), c.ParamInt("id", 0))
	if err != nil && !errors.Is(err, models.ErrNotFound) {
		panic(err)
	}
	c.Redirect("/order")
}

// itemsFromForm reads name1/qty1/price1 ... name3/qty3/price3. Fully empty rows are skipped.
// Unreadable numbers become values the model rejects (qty 0, negative price).
func (c *Order) itemsFromForm() []models.Item {
	var items []models.Item
	for i := 1; i <= formRows; i++ {
		name := strings.TrimSpace(c.Param(fmt.Sprintf("name%d", i), ""))
		qty := c.Param(fmt.Sprintf("qty%d", i), "")
		price := c.Param(fmt.Sprintf("price%d", i), "")
		if name == "" && qty == "" && price == "" {
			continue
		}
		p, ok := models.ParseMoney(price)
		if !ok {
			p = -1
		}
		items = append(items, models.Item{Name: name, Qty: c.ParamInt(fmt.Sprintf("qty%d", i), 0), Price: p})
	}
	return items
}
