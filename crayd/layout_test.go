package crayd

import (
	"strings"
	"testing"
)

type layoutPage struct{ Controller }

func (c *layoutPage) Index() {}
func (c *layoutPage) Bare()  { c.SetLayout("") }
func (c *layoutPage) Admin() { c.SetLayout("admin") }
func (c *layoutPage) Gone()  { c.SetLayout("missing") }

type layoutShop struct{ Controller }

func (c *layoutShop) PreDispatch() { c.SetLayout("shop") }
func (c *layoutShop) Index()       {}
func (c *layoutShop) Plain()       { c.SetLayout("default") } // action overrides the controller

func newLayoutApp(t *testing.T) *devApp {
	d := newDevApp(t, false)
	d.write("layout.html", `[{{ template "_nav" . }}|{{ block "title" . }}Site{{ end }}|{{ template "content" . }}]`)
	d.write("layout_admin.html", `ADMIN({{ template "content" . }})`)
	d.write("layout_shop.html", `SHOP({{ template "content" . }})`)
	d.write("_nav.html", `nav`)
	d.write("page_index.html", `{{ define "title" }}Home{{ end }}index`)
	d.write("page_bare.html", `bare {{ template "_nav" . }}`)
	d.write("page_admin.html", `admin`)
	d.write("page_gone.html", `gone`)
	d.write("shop_index.html", `shop`)
	d.write("shop_plain.html", `plain`)
	d.write("about_index.html", `about`)
	d.app = New(d.dir)
	d.app.Static("")
	d.app.Register("page", &layoutPage{})
	d.app.Register("shop", &layoutShop{})
	return d
}

func TestLayouts(t *testing.T) {
	d := newLayoutApp(t)
	for _, tc := range []struct{ path, want string }{
		{"/page", "[nav|Home|index]"},       // default layout, partial, view overrides a block
		{"/page/bare", "bare nav"},          // no layout, partial still works
		{"/page/admin", "ADMIN(admin)"},     // named layout from an action
		{"/shop", "SHOP(shop)"},             // controller-wide layout from PreDispatch
		{"/shop/plain", "[nav|Site|plain]"}, // action switches back to the default
		{"/about", "[nav|Site|about]"},      // view-only page uses the default layout
	} {
		if code, body := d.get(tc.path); code != 200 || body != tc.want {
			t.Errorf("%s: %d %q, want %q", tc.path, code, body, tc.want)
		}
	}
	if code, _ := d.get("/page/gone"); code != 500 {
		t.Errorf("missing layout: %d, want 500", code)
	}
}

func TestLayoutsAndPartialsAreNotPages(t *testing.T) {
	d := newLayoutApp(t)
	for _, p := range []string{"/layout/admin", "/layout", "/_nav"} {
		if code, _ := d.get(p); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	for _, r := range d.app.Routes() {
		if strings.Contains(r, "/layout") || strings.Contains(r, "layout_") || strings.Contains(r, "_nav") {
			t.Errorf("route table lists %q", r)
		}
	}
}

func TestLayoutErrorsNameTheFile(t *testing.T) {
	d := newDevApp(t, false)
	d.write("layout_admin.html", `{{ template "content" . `)
	if _, err := parseViews(d.dir); err == nil || !strings.Contains(err.Error(), "layout_admin.html") {
		t.Fatalf("got %v", err)
	}
	d.write("layout_admin.html", `ok`)
	d.write("_nav.html", `{{ if }}`)
	if _, err := parseViews(d.dir); err == nil || !strings.Contains(err.Error(), "_nav.html") {
		t.Fatalf("got %v", err)
	}
}

func TestDevReloadPicksUpNewLayout(t *testing.T) {
	d := newLayoutApp(t)
	d.app.Dev(true)
	d.write("layout_admin.html", `NEW({{ template "content" . }})`)
	if _, body := d.get("/page/admin"); body != "NEW(admin)" {
		t.Fatalf("%q", body)
	}
}
