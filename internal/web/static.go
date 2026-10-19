package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Static asset caching: versioned URLs are immutable, the rest revalidates.
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheRevalid   = "no-cache"
	staticPrefix   = "/static/"
	indexName      = "index.html"
	etagHexLen     = 16
)

type asset struct {
	body    []byte
	etag    string // quoted
	version string
	ctype   string
}

// Static serves the embedded web client from memory.
type Static struct {
	files map[string]*asset
	index *asset
}

func contentType(name string) (string, bool) {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8", true
	case ".css":
		return "text/css; charset=utf-8", true
	case ".js":
		return "text/javascript; charset=utf-8", true
	case ".svg":
		return "image/svg+xml", true
	default:
		return "", false
	}
}

func newAsset(name string, body []byte) (*asset, error) {
	ct, ok := contentType(name)
	if !ok {
		return nil, fmt.Errorf("web: unsupported static file %q", name)
	}
	sum := sha256.Sum256(body)
	v := hex.EncodeToString(sum[:])[:etagHexLen]
	return &asset{body: body, etag: `"` + v + `"`, version: v, ctype: ct}, nil
}

// NewStatic loads every file of dir in fsys. References to /static/<name>
// in index.html are rewritten to versioned URLs.
func NewStatic(fsys fs.FS, dir string) (*Static, error) {
	s := &Static{files: make(map[string]*asset)}
	var indexBody []byte
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("web: read %s: %w", p, err)
		}
		name := strings.TrimPrefix(p, dir+"/")
		if name == indexName {
			indexBody = body
			return nil
		}
		a, err := newAsset(name, body)
		if err != nil {
			return err
		}
		s.files[name] = a
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: load static files: %w", err)
	}
	if indexBody == nil {
		return nil, errors.New("web: index.html missing")
	}
	html := string(indexBody)
	for name, a := range s.files {
		ref := `"` + staticPrefix + name + `"`
		html = strings.ReplaceAll(html, ref, `"`+staticPrefix+name+"?v="+a.version+`"`)
	}
	if s.index, err = newAsset(indexName, []byte(html)); err != nil {
		return nil, err
	}
	return s, nil
}

// Size returns the total size of the client in bytes.
func (s *Static) Size() int {
	n := len(s.index.body)
	for _, a := range s.files {
		n += len(a.body)
	}
	return n
}

func serveAsset(w http.ResponseWriter, r *http.Request, a *asset, cache string) {
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	h.Set("Cache-Control", cache)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(a.body) // client gone: nothing to do
}

// Index serves the client page.
func (s *Static) Index(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, s.index, cacheRevalid)
}

// File serves /static/{name}.
func (s *Static) File(w http.ResponseWriter, r *http.Request) {
	a, ok := s.files[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	cache := cacheRevalid
	if r.URL.Query().Get("v") == a.version {
		cache = cacheImmutable
	}
	serveAsset(w, r, a, cache)
}
