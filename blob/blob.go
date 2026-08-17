// Package blob is the storage seam for user-visible uploaded files
// (avatars, wiki images, community banners, admin-managed covers).
//
// Callers never build filesystem paths or public URLs themselves:
// they Save bytes under a store-relative name like
// "wiki-uploads/9f0c….png" and get back the URL to serve. The
// production implementation is Local (a directory under the host's
// static root); a remote file-hosting service becomes a second
// implementation swapped in at wiring time, which is the entire
// migration story for moving uploads off the web box.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Store abstracts where uploaded files live and how they are addressed
// publicly. Names are store-relative, forward-slash paths ("avatars/12.jpg").
type Store interface {
	// Save writes data under name, replacing any existing file, and
	// returns the public URL for it.
	Save(ctx context.Context, name string, data []byte) (string, error)

	// Remove deletes the named file. Removing a file that does not
	// exist is not an error — the caller's intent (name gone) holds.
	Remove(ctx context.Context, name string) error

	// List returns the stored names under prefix (a directory-style
	// namespace like "achievement-badges/"), each usable with Save and
	// Remove, alongside its public URL. An empty prefix lists everything,
	// which a caller should think twice about wanting.
	//
	// Added for "pick an existing image" controls: without it every
	// upload UI could only ever add, and re-using an image meant
	// re-uploading it under a second name. Order is lexical, for stable
	// dropdowns rather than filesystem luck.
	List(ctx context.Context, prefix string) ([]Entry, error)
}

// Entry is one stored file, as List reports it.
type Entry struct {
	// Name is the store-relative name (what Save took, what Remove takes).
	Name string
	// URL is the public path the host serves it under.
	URL string
}

// ImageExts maps the sniffed MIME types accepted for user image
// uploads to their canonical file extension. This is the single
// source of truth — wiki, community, and admin upload paths each
// carried a hand-copied version of this map before it moved here.
var ImageExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// SniffImage detects an upload's MIME type from its first 512 bytes
// (http.DetectContentType — never the client-supplied filename) and
// returns the MIME plus canonical extension. Errors when the payload
// is not an accepted web image; the message is safe to show clients.
func SniffImage(data []byte) (mime, ext string, err error) {
	if len(data) == 0 {
		return "", "", errors.New("empty file")
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	m := http.DetectContentType(sniff)
	e, ok := ImageExts[m]
	if !ok {
		return "", "", fmt.Errorf("unsupported file type: %s", m)
	}
	return m, e, nil
}

// Local is the host-filesystem Store: files land under root and are
// served by the host's static handler under urlPrefix.
type Local struct {
	root      string
	urlPrefix string
}

// NewLocal builds a Local store. root is the on-disk directory backing
// the store (e.g. "web/static"); urlPrefix is the public prefix the
// host serves that directory under (e.g. "/static/").
func NewLocal(root, urlPrefix string) *Local {
	if !strings.HasSuffix(urlPrefix, "/") {
		urlPrefix += "/"
	}
	return &Local{root: root, urlPrefix: urlPrefix}
}

// Save writes data to root/name via a temp file + rename so readers
// never observe a partially written file, and returns urlPrefix+name.
func (l *Local) Save(ctx context.Context, name string, data []byte) (string, error) {
	clean, err := cleanName(name)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(l.root, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("blob: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".tmp*")
	if err != nil {
		return "", fmt.Errorf("blob: create: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("blob: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("blob: close: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("blob: chmod: %w", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("blob: rename: %w", err)
	}
	return l.urlPrefix + clean, nil
}

// Remove deletes root/name. A missing file is success.
func (l *Local) Remove(ctx context.Context, name string) error {
	clean, err := cleanName(name)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(l.root, filepath.FromSlash(clean)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: remove: %w", err)
	}
	return nil
}

// cleanName validates a store-relative name. Names are the one input
// that can reach the filesystem from user-influenced code (filenames,
// route params), so this is the traversal boundary: forward-slash
// relative paths only, no "..", no absolute paths, no backslashes.
// ":" is rejected too — on Windows it is a drive prefix or an NTFS
// alternate-data-stream separator; nothing escapes the root either
// way, but no legitimate store name contains one. Control characters
// likewise.
func cleanName(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.ContainsAny(name, "\\:") {
		return "", fmt.Errorf("blob: invalid name %q", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("blob: invalid name %q", name)
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("blob: invalid name %q", name)
	}
	return clean, nil
}

// List walks root/prefix, reporting each regular file store-relative.
// A missing prefix directory is an empty listing, not an error — asking
// "what is here" about a namespace nobody has written to has an answer.
func (l *Local) List(ctx context.Context, prefix string) ([]Entry, error) {
	clean := ""
	if prefix != "" {
		c, err := cleanName(strings.TrimSuffix(prefix, "/"))
		if err != nil {
			return nil, err
		}
		clean = c
	}
	dir := filepath.Join(l.root, filepath.FromSlash(clean))
	var out []Entry
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		out = append(out, Entry{Name: name, URL: l.urlPrefix + name})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("blob: list %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
