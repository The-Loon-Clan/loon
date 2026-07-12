package core

import (
	"html/template"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterAdminView(t *testing.T) {
	c := &Core{}
	render := func(_ *gin.Context) (template.HTML, error) { return "<p>hi</p>", nil }

	if err := c.RegisterAdminView(AdminView{Slug: "usenet", Title: "Usenet", Kind: ViewSettings, Render: render}); err != nil {
		t.Fatal(err)
	}
	// missing pieces rejected
	if err := c.RegisterAdminView(AdminView{Slug: "", Title: "x", Render: render}); err == nil {
		t.Fatal("empty slug accepted")
	}
	if err := c.RegisterAdminView(AdminView{Slug: "x", Title: "x"}); err == nil {
		t.Fatal("nil Render accepted")
	}
	// duplicate slug rejected
	if err := c.RegisterAdminView(AdminView{Slug: "usenet", Title: "Again", Render: render}); err == nil {
		t.Fatal("duplicate slug accepted")
	}
	// default kind + registration order preserved
	if err := c.RegisterAdminView(AdminView{Slug: "crawlers", Title: "Crawlers", Render: render}); err != nil {
		t.Fatal(err)
	}
	vs := c.AdminViews()
	if len(vs) != 2 || vs[0].Slug != "usenet" || vs[1].Slug != "crawlers" {
		t.Fatalf("views = %+v", vs)
	}
	if vs[1].Kind != ViewPage {
		t.Fatalf("default kind = %q, want %q", vs[1].Kind, ViewPage)
	}
}
