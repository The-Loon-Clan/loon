package core

import (
	"html/template"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterView(t *testing.T) {
	c := &Core{}
	render := func(_ *gin.Context) (template.HTML, error) { return "<p>hi</p>", nil }

	if err := c.RegisterView(View{Slug: "usenet", Title: "Usenet", Slot: SlotAdminSettings, Render: render}); err != nil {
		t.Fatal(err)
	}
	// missing pieces rejected
	if err := c.RegisterView(View{Slug: "", Title: "x", Slot: SlotAdminPage, Render: render}); err == nil {
		t.Fatal("empty slug accepted")
	}
	if err := c.RegisterView(View{Slug: "x", Title: "x", Render: render}); err == nil {
		t.Fatal("empty slot accepted")
	}
	if err := c.RegisterView(View{Slug: "x", Title: "x", Slot: SlotSitePage}); err == nil {
		t.Fatal("nil Render accepted")
	}
	// (slot, slug) duplicate rejected; same slug in a DIFFERENT slot is fine
	if err := c.RegisterView(View{Slug: "usenet", Title: "Again", Slot: SlotAdminSettings, Render: render}); err == nil {
		t.Fatal("duplicate (slot,slug) accepted")
	}
	if err := c.RegisterView(View{Slug: "usenet", Title: "Usenet jobs", Slot: SlotJobsWidget, Anchor: "Usenet", Render: render}); err != nil {
		t.Fatal(err)
	}

	// slot filtering + order
	if vs := c.Views(SlotAdminSettings); len(vs) != 1 || vs[0].Slug != "usenet" {
		t.Fatalf("settings views = %+v", vs)
	}
	if vs := c.Views(SlotJobsWidget); len(vs) != 1 || vs[0].Anchor != "Usenet" {
		t.Fatalf("jobs widgets = %+v", vs)
	}
	if vs := c.Views(SlotSitePage); len(vs) != 0 {
		t.Fatalf("site pages = %+v", vs)
	}
}

func TestViewVisibility(t *testing.T) {
	pub := View{Public: true}
	member := View{} // zero MinRole = RoleUser: any account
	ranked := View{MinRole: RoleContributor}

	if !pub.AllowsUser(nil) || !pub.AllowsAnon() {
		t.Fatal("public view should allow anonymous")
	}
	if member.AllowsUser(nil) || member.AllowsAnon() {
		t.Fatal("member view allowed anonymous")
	}
	u := &User{Role: RoleUser}
	if !member.AllowsUser(u) {
		t.Fatal("member view rejected a logged-in user")
	}
	if ranked.AllowsUser(u) {
		t.Fatal("ranked view allowed a below-rank user")
	}
	if !ranked.AllowsUser(&User{Role: RoleContributor}) || !ranked.AllowsUser(&User{Role: RoleAdmin}) {
		t.Fatal("ranked view rejected an at/above-rank user")
	}
}

func TestViewSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	if _, ok := ViewSubject(c); ok {
		t.Fatal("subject present before SetViewSubject")
	}
	SetViewSubject(c, 42)
	if id, ok := ViewSubject(c); !ok || id != 42 {
		t.Fatalf("subject = %d/%v, want 42/true", id, ok)
	}
}
