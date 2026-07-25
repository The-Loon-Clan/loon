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
func cleanName(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "", fmt.Errorf("blob: invalid name %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("blob: invalid name %q", name)
	}
	return clean, nil
}
