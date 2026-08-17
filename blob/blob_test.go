package blob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSniffImage(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantExt string
		wantErr bool
	}{
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), ".png", false},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, ".jpg", false},
		{"gif", []byte("GIF89a......"), ".gif", false},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp", false},
		{"text", []byte("just some text, definitely not an image"), "", true},
		{"empty", nil, "", true},
		{"html", []byte("<!doctype html><script>x</script>"), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, ext, err := SniffImage(tc.data)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got mime=%s ext=%s", mime, ext)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ext != tc.wantExt {
				t.Fatalf("ext = %s, want %s", ext, tc.wantExt)
			}
			if ImageExts[mime] != ext {
				t.Fatalf("mime %s does not map to ext %s", mime, ext)
			}
		})
	}
}

func TestLocalSaveRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := NewLocal(root, "/static") // missing trailing slash must be tolerated
	ctx := context.Background()

	url, err := s.Save(ctx, "wiki-uploads/a.png", []byte("payload"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if url != "/static/wiki-uploads/a.png" {
		t.Fatalf("url = %s", url)
	}
	got, err := os.ReadFile(filepath.Join(root, "wiki-uploads", "a.png"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("content = %q", got)
	}

	// Overwrite replaces content.
	if _, err := s.Save(ctx, "wiki-uploads/a.png", []byte("v2")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(root, "wiki-uploads", "a.png"))
	if string(got) != "v2" {
		t.Fatalf("after overwrite content = %q", got)
	}

	// No leftover temp files after saves.
	entries, _ := os.ReadDir(filepath.Join(root, "wiki-uploads"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestLocalRemove(t *testing.T) {
	root := t.TempDir()
	s := NewLocal(root, "/static/")
	ctx := context.Background()

	if _, err := s.Save(ctx, "avatars/1.jpg", []byte("x")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Remove(ctx, "avatars/1.jpg"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "avatars", "1.jpg")); !os.IsNotExist(err) {
		t.Fatal("file still exists")
	}
	// Removing a missing file is success, not an error.
	if err := s.Remove(ctx, "avatars/1.jpg"); err != nil {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestLocalRejectsHostileNames(t *testing.T) {
	root := t.TempDir()
	s := NewLocal(root, "/static/")
	ctx := context.Background()

	hostile := []string{
		"",
		"..",
		"../evil.png",
		"a/../../evil.png",
		"/etc/passwd",
		"a\\b.png",
		"dir/",
		"C:foo.png",
		"x.jpg:evil",
		"a/\x00b.png",
		"a/\x1bb.png",
	}
	for _, name := range hostile {
		if _, err := s.Save(ctx, name, []byte("x")); err == nil {
			t.Errorf("Save(%q) accepted a hostile name", name)
		}
		if err := s.Remove(ctx, name); err == nil {
			t.Errorf("Remove(%q) accepted a hostile name", name)
		}
	}

	// Nothing escaped the root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.png")); !os.IsNotExist(err) {
		t.Fatal("a hostile name escaped the store root")
	}
}

// List reports what Save stored, under the asked-for namespace only, in a
// stable order — and an unwritten namespace is an empty answer, not an error.
func TestListReportsSavedFilesUnderPrefix(t *testing.T) {
	dir := t.TempDir()
	l := NewLocal(dir, "/uploads")
	ctx := context.Background()
	for _, name := range []string{"badges/b.png", "badges/a.png", "covers/c.png"} {
		if _, err := l.Save(ctx, name, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.List(ctx, "badges/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "badges/a.png" || got[1].Name != "badges/b.png" {
		t.Fatalf("List(badges/) = %+v; want a.png then b.png, covers excluded", got)
	}
	if got[0].URL != "/uploads/badges/a.png" {
		t.Fatalf("URL = %q", got[0].URL)
	}
	if empty, err := l.List(ctx, "nothing-here/"); err != nil || len(empty) != 0 {
		t.Fatalf("unwritten namespace: %v, %v; want empty, nil", empty, err)
	}
	// Traversal stays refused at the same boundary Save uses.
	if _, err := l.List(ctx, "../"); err == nil {
		t.Fatal("List accepted a traversal prefix")
	}
}
