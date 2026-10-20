package files

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/draw"
)

// Profile pictures (ADR 0012): kept in RAM while their owner is connected,
// one per identity, always re-encoded to a small square JPEG.
const (
	AvatarSize      = 128
	MaxAvatarUpload = 4 << 20
	maxAvatars      = 2048
	avatarQuality   = 85
)

var errNotAnImage = errors.New("files: a picture must be an image")

type avatar struct {
	data    []byte
	version int
}

// Avatars stores one picture per author identity.
type Avatars struct {
	mu      sync.Mutex
	byOwner map[string]avatar
	seq     int
}

// NewAvatars returns an empty store.
func NewAvatars() *Avatars { return &Avatars{byOwner: make(map[string]avatar)} }

// Version returns the version of owner's picture, 0 if none.
func (a *Avatars) Version(owner string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byOwner[owner].version
}

func (a *Avatars) get(owner string) (avatar, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.byOwner[owner]
	return v, ok
}

func (a *Avatars) set(owner string, data []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.byOwner[owner]; !exists && len(a.byOwner) >= maxAvatars {
		return 0, errors.New("files: too many pictures")
	}
	a.seq++
	a.byOwner[owner] = avatar{data: data, version: a.seq}
	return a.seq, nil
}

// Delete removes owner's picture.
func (a *Avatars) Delete(owner string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byOwner, owner)
}

// Len returns the number of pictures held.
func (a *Avatars) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.byOwner)
}

// MakeAvatar sanitizes an uploaded image and turns it into a centered
// AvatarSize square JPEG. Non-images are refused.
func MakeAvatar(data []byte) ([]byte, error) {
	res, err := Sanitize(data)
	if err != nil {
		return nil, err
	}
	if !res.Image {
		return nil, errNotAnImage
	}
	src, _, err := image.Decode(bytes.NewReader(res.Data))
	if err != nil {
		return nil, ErrBadImage
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	crop := image.Rect(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2, 0, 0)
	crop.Max = crop.Min.Add(image.Pt(side, side))
	dst := image.NewRGBA(image.Rect(0, 0, AvatarSize, AvatarSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: avatarQuality}); err != nil {
		return nil, fmt.Errorf("files: encode picture: %w", err)
	}
	return out.Bytes(), nil
}

// AvatarHandler serves profile pictures.
type AvatarHandler struct {
	avatars *Avatars
	tokens  *Tokens
	h       *Handler
	ownerOf func(nick string) (string, bool)
	changed func(owner string)
}

// NewAvatarHandler wires the picture endpoints. ownerOf resolves a nick to
// its author identity, changed is called after an upload or a removal.
func NewAvatarHandler(h *Handler, a *Avatars, ownerOf func(string) (string, bool), changed func(string)) *AvatarHandler {
	return &AvatarHandler{avatars: a, tokens: h.tokens, h: h, ownerOf: ownerOf, changed: changed}
}

// Upload handles POST /avatar (raw image body, Bearer upload token).
func (a *AvatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	owner, ok := a.tokens.Owner(token)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fugace"`)
		plainError(w, "upload token missing or not bound to a live connection", http.StatusUnauthorized)
		return
	}
	if allowed, wait := a.tokens.Allow(token, a.h.now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		plainError(w, "too many uploads, retry later", http.StatusTooManyRequests)
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > MaxAvatarUpload {
		plainError(w, "picture missing or too large", http.StatusRequestEntityTooLarge)
		return
	}
	data := make([]byte, r.ContentLength)
	if _, err := io.ReadFull(http.MaxBytesReader(w, r.Body, MaxAvatarUpload), data); err != nil {
		plainError(w, "incomplete body", http.StatusBadRequest)
		return
	}
	select {
	case a.h.decodes <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	pic, err := MakeAvatar(data)
	<-a.h.decodes
	switch {
	case errors.Is(err, ErrTooManyPixels):
		plainError(w, "image has too many pixels", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		plainError(w, "a picture must be a valid image", http.StatusUnsupportedMediaType)
		return
	}
	version, err := a.avatars.set(owner, pic)
	if err != nil {
		plainError(w, "server busy, retry later", http.StatusServiceUnavailable)
		return
	}
	a.changed(owner)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, `{"version":`+strconv.Itoa(version)+"}\n") // client gone: nothing to do
}

// Delete handles DELETE /avatar.
func (a *AvatarHandler) Delete(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.tokens.Owner(bearer(r))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fugace"`)
		plainError(w, "upload token missing or not bound to a live connection", http.StatusUnauthorized)
		return
	}
	a.avatars.Delete(owner)
	a.changed(owner)
	w.WriteHeader(http.StatusNoContent)
}

// Serve handles GET /avatar/{nick}?t=<token>[&v=<version>]. Only connected
// users can see pictures: the token is required, so they cannot be
// enumerated from outside.
func (a *AvatarHandler) Serve(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.tokens.Owner(r.URL.Query().Get("t")); !ok {
		plainError(w, "not connected", http.StatusUnauthorized)
		return
	}
	owner, ok := a.ownerOf(r.PathValue("nick"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	pic, ok := a.avatars.get(owner)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", TypeJPEG)
	hdr.Set("Content-Security-Policy", FileCSP)
	hdr.Set("Content-Length", strconv.Itoa(len(pic.data)))
	hdr.Set("ETag", `"a`+strconv.Itoa(pic.version)+`"`)
	if r.URL.Query().Get("v") == strconv.Itoa(pic.version) {
		hdr.Set("Cache-Control", "private, max-age=86400, immutable")
	} else {
		hdr.Set("Cache-Control", "private, no-cache")
	}
	if strings.Contains(r.Header.Get("If-None-Match"), hdr.Get("ETag")) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(pic.data) // client gone: nothing to do
}
