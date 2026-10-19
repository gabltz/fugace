package files

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FileCSP is the policy of every served file (ARCHITECTURE §7).
const FileCSP = "default-src 'none'; img-src 'self'; sandbox"

// MaxDecodes bounds concurrent image decodes: a 25 MP image needs about
// 100 MB of RAM once decoded.
const MaxDecodes = 1

const bearerPrefix = "Bearer "

// Handler serves uploads and downloads.
type Handler struct {
	store     *Store
	tokens    *Tokens
	maxUpload int64
	base      func(r *http.Request) string // absolute URL base, no trailing slash
	now       func() time.Time
	decodes   chan struct{}
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	Store     *Store
	Tokens    *Tokens
	MaxUpload int64
	// PublicURL is the base of returned URLs; when nil, the request's
	// scheme and Host are used.
	PublicURL *url.URL
	Now       func() time.Time
}

// NewHandler returns the HTTP side of file sharing.
func NewHandler(o HandlerOptions) *Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	h := &Handler{store: o.Store, tokens: o.Tokens, maxUpload: o.MaxUpload, now: o.Now, decodes: make(chan struct{}, MaxDecodes)}
	h.base = func(r *http.Request) string {
		if o.PublicURL != nil {
			return strings.TrimSuffix(o.PublicURL.String(), "/")
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	return h
}

func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(v[len(bearerPrefix):])
}

// UploadReply is the JSON answer to a successful upload.
type UploadReply struct {
	URL     string `json:"url"`
	ID      string `json:"id"`
	Expires string `json:"expires"`
}

func plainError(w http.ResponseWriter, msg string, code int) {
	http.Error(w, msg, code)
}

// Upload handles POST /upload?name=<name> with the raw file as body.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	owner, ok := h.tokens.Owner(token)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fugace"`)
		plainError(w, "upload token missing or not bound to a live connection", http.StatusUnauthorized)
		return
	}
	if allowed, wait := h.tokens.Allow(token, h.now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		plainError(w, "too many uploads, retry later", http.StatusTooManyRequests)
		return
	}
	switch {
	case r.ContentLength < 0:
		plainError(w, "Content-Length required", http.StatusLengthRequired)
		return
	case r.ContentLength == 0:
		plainError(w, "empty file", http.StatusBadRequest)
		return
	case r.ContentLength > h.maxUpload:
		plainError(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	// Raw body into a bounded buffer: never ParseMultipartForm (disk spill).
	body := http.MaxBytesReader(w, r.Body, h.maxUpload)
	data := make([]byte, r.ContentLength)
	if _, err := io.ReadFull(body, data); err != nil {
		plainError(w, "incomplete body", http.StatusBadRequest)
		return
	}
	select {
	case h.decodes <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	res, err := Sanitize(data)
	<-h.decodes
	switch {
	case errors.Is(err, ErrTooManyPixels):
		plainError(w, "image has too many pixels", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		plainError(w, "invalid image", http.StatusUnsupportedMediaType)
		return
	}
	f, err := h.store.Put(SanitizeName(r.URL.Query().Get("name"), res.Ext), owner, res)
	if err != nil {
		plainError(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	reply := UploadReply{
		URL:     h.base(r) + "/f/" + f.ID + "/" + url.PathEscape(f.Name),
		ID:      f.ID,
		Expires: f.Expires.UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(reply) // client gone: nothing to do
}

// Serve handles GET/HEAD /f/{id}/{name}.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	f, ok := h.store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := f.Data // read once: an eviction may clear f.Data meanwhile
	hdr := w.Header()
	hdr.Set("Content-Security-Policy", FileCSP)
	hdr.Set("Content-Type", f.Type)
	hdr.Set("Content-Length", strconv.Itoa(len(data)))
	remaining := int(f.Expires.Sub(h.now()).Seconds())
	hdr.Set("Cache-Control", "private, max-age="+strconv.Itoa(max(remaining, 0)))
	disposition := "attachment"
	if f.Image {
		disposition = "inline"
	}
	hdr.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data) // client gone: nothing to do
}

// Delete handles DELETE /f/{id}[/{name}] by the uploader.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.tokens.Owner(bearer(r))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fugace"`)
		plainError(w, "upload token missing or not bound to a live connection", http.StatusUnauthorized)
		return
	}
	switch err := h.store.Delete(r.PathValue("id"), owner); {
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, ErrForbidden):
		plainError(w, "only the uploader can delete this file", http.StatusForbidden)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
