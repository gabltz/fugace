package files

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- fixtures -----------------------------------------------------------

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 40), uint8(y * 40), 200, 255})
		}
	}
	img.Set(0, 0, color.RGBA{255, 0, 0, 255}) // marker in the top-left corner
	return img
}

// exifAPP1 builds an APP1 segment with Orientation and a GPS-looking ASCII
// tag carrying secret.
func exifAPP1(orientation uint16, secret string) []byte {
	var t bytes.Buffer
	t.WriteString("II*\x00")
	_ = binary.Write(&t, binary.LittleEndian, uint32(8)) // IFD0 offset
	_ = binary.Write(&t, binary.LittleEndian, uint16(2)) // entries
	// Orientation, SHORT, count 1, value inline
	_ = binary.Write(&t, binary.LittleEndian, []uint16{0x0112, 3})
	_ = binary.Write(&t, binary.LittleEndian, uint32(1))
	_ = binary.Write(&t, binary.LittleEndian, []uint16{orientation, 0})
	// ImageDescription, ASCII, pointing after the IFD
	_ = binary.Write(&t, binary.LittleEndian, []uint16{0x010E, 2})
	_ = binary.Write(&t, binary.LittleEndian, uint32(len(secret)+1))
	_ = binary.Write(&t, binary.LittleEndian, uint32(8+2+2*12+4))
	_ = binary.Write(&t, binary.LittleEndian, uint32(0)) // next IFD
	t.WriteString(secret + "\x00")
	payload := append([]byte("Exif\x00\x00"), t.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

func jpegWithEXIF(t *testing.T, w, h int, orientation uint16, secret string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	out := append([]byte{}, raw[:2]...) // SOI
	out = append(out, exifAPP1(orientation, secret)...)
	return append(out, raw[2:]...)
}

func pngChunk(typ string, data []byte) []byte {
	c := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(c, uint32(len(data)))
	copy(c[4:], typ)
	c = append(c, data...)
	crc := crc32.ChecksumIEEE(c[4:])
	return binary.BigEndian.AppendUint32(c, crc)
}

func pngWithText(t *testing.T, secret string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(4, 3)); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	const ihdrEnd = 8 + 25
	out := append([]byte{}, raw[:ihdrEnd]...)
	out = append(out, pngChunk("tEXt", []byte("Comment\x00"+secret))...)
	return append(out, raw[ihdrEnd:]...)
}

// pngBomb has only a header claiming w x h pixels.
func pngBomb(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	out := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	return append(out, pngChunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
}

func animatedGIF(t *testing.T, frames int, comment string) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{LoopCount: 0}
	for i := range frames {
		p := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
		p.SetColorIndex(i%4, 0, 1)
		g.Image = append(g.Image, p)
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	if comment == "" {
		return raw
	}
	// Comment extension right after the header and global color table.
	head := 13
	if raw[10]&0x80 != 0 {
		head += 3 << (int(raw[10]&7) + 1)
	}
	ext := append([]byte{0x21, 0xFE, byte(len(comment))}, comment...)
	ext = append(ext, 0)
	out := append([]byte{}, raw[:head]...)
	out = append(out, ext...)
	return append(out, raw[head:]...)
}

// A 1x1 lossless WebP.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

// --- sanitize -----------------------------------------------------------

func TestSanitizeStripsMetadata(t *testing.T) {
	const secret = "GPS-48.8584N-2.2945E"
	tests := []struct {
		name string
		in   []byte
		typ  string
		ext  string
	}{
		{"jpeg with exif", jpegWithEXIF(t, 6, 4, 1, secret), TypeJPEG, ".jpg"},
		{"png with text", pngWithText(t, secret), TypePNG, ".png"},
		{"gif with comment", animatedGIF(t, 3, secret), TypeGIF, ".gif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Contains(tt.in, []byte(secret)) {
				t.Fatal("fixture lacks the secret")
			}
			res, err := Sanitize(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Type != tt.typ || res.Ext != tt.ext || !res.Image {
				t.Fatalf("result = %s %s %v", res.Type, res.Ext, res.Image)
			}
			if bytes.Contains(res.Data, []byte(secret)) || bytes.Contains(res.Data, []byte("Exif")) || bytes.Contains(res.Data, []byte("tEXt")) {
				t.Fatal("metadata survived re-encoding")
			}
		})
	}
}

func TestSanitizeKeepsAnimation(t *testing.T) {
	res, err := Sanitize(animatedGIF(t, 5, "c"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(res.Data))
	if err != nil || len(g.Image) != 5 || g.Delay[0] != 10 {
		t.Fatalf("animation lost: %v", err)
	}
}

func TestSanitizeOrientation(t *testing.T) {
	for o := uint16(1); o <= 8; o++ {
		res, err := Sanitize(jpegWithEXIF(t, 6, 4, o, "x"))
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatal(err)
		}
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		if swapped := o >= 5; (w == 4 && h == 6) != swapped {
			t.Errorf("orientation %d: %dx%d", o, w, h)
		}
	}
}

func TestSanitizeWebP(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString(tinyWebP)
	res, err := Sanitize(data)
	if err != nil || res.Type != TypePNG || res.Ext != ".png" {
		t.Fatalf("webp: %+v %v", res, err)
	}
	if _, err := png.Decode(bytes.NewReader(res.Data)); err != nil {
		t.Fatal(err)
	}
}

// T5: oversized dimensions are refused before any pixel is decoded.
func TestDecompressionBombs(t *testing.T) {
	if _, err := Sanitize(pngBomb(50000, 50000)); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("png bomb: %v", err)
	}
	if _, err := Sanitize(pngBomb(5000, 5001)); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("25 MP + 1 row: %v", err)
	}
	if _, err := Sanitize(animatedGIF(t, MaxGIFFrames+1, "")); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("gif frame bomb: %v", err)
	}
	// Few frames claiming a huge cumulated area: 5 x (5000 x 5000).
	big := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00")
	frame := []byte{0x2C, 0, 0, 0, 0, 0x88, 0x13, 0x88, 0x13, 0x00, 0x02, 0x00} // no pixel data
	for range 5 {
		big = append(big, frame...)
	}
	big = append(big, 0x3B)
	if err := checkGIFFrames(big); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("gif area bomb: %v", err)
	}
}

// errRefused marks a case where any refusal (bad image, too many pixels) is right.
var errRefused = errors.New("refused")

// T4: nothing executable is ever served as an image.
func TestPolyglotsAndScripts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		err  error
		typ  string
	}{
		{"gif/html polyglot", "GIF89a<html><script>alert(1)</script></html>", errRefused, ""},
		{"svg with script", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, nil, TypeBinary},
		{"html", "<!doctype html><script>alert(1)</script>", nil, TypeBinary},
		{"png header then html", "\x89PNG\r\n\x1a\n<script>", errRefused, ""},
		{"pdf", "%PDF-1.7\n", nil, TypeBinary},
	}
	for _, tt := range tests {
		res, err := Sanitize([]byte(tt.in))
		refused := errors.Is(err, ErrBadImage) || errors.Is(err, ErrTooManyPixels)
		if errors.Is(tt.err, errRefused) != refused || (err == nil && (res.Type != tt.typ || res.Image || res.Ext != "")) {
			t.Errorf("%s: %+v %v", tt.name, res.Type, err)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct{ in, ext, want string }{
		{"photo.JPG", ".jpg", "photo.jpg"},
		{"../../etc/passwd", "", "passwd"},
		{`C:\Users\me\holiday pic.webp`, ".png", "holiday_pic.png"},
		{"<script>.svg", "", "script_.svg"},
		{"été 2026.pdf", "", "t__2026.pdf"},
		{"", "", "file"},
		{"...", ".gif", "file.gif"},
		{".htaccess", "", "htaccess"},
		{strings.Repeat("a", 200) + ".txt", "", strings.Repeat("a", maxNameLen)},
		{strings.Repeat("b", 200), ".jpg", strings.Repeat("b", maxNameLen-4) + ".jpg"},
		{"x\r\nSet-Cookie: a", "", "x__Set-Cookie__a"},
	}
	for _, tt := range tests {
		if got := SanitizeName(tt.in, tt.ext); got != tt.want {
			t.Errorf("SanitizeName(%q, %q) = %q, want %q", tt.in, tt.ext, got, tt.want)
		}
	}
}

// --- store and tokens ---------------------------------------------------

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestStore(t *testing.T) {
	c := &clock{time.Unix(1_000_000, 0)}
	s := NewStore(10, time.Hour, c.now)
	put := func(n int, owner string) *File {
		t.Helper()
		f, err := s.Put("f", owner, Result{Data: make([]byte, n), Type: TypeBinary})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a := put(4, "o1")
	if len(a.ID) != 22 || !a.Expires.Equal(c.t.Add(time.Hour)) {
		t.Fatalf("id %q expires %v", a.ID, a.Expires)
	}
	b := put(4, "o1")
	put(4, "o2") // over budget: a (the oldest) is evicted
	if _, ok := s.Get(a.ID); ok {
		t.Fatal("oldest file not evicted")
	}
	if used, n := s.Usage(); used != 8 || n != 2 {
		t.Fatalf("usage %d/%d", used, n)
	}
	if _, err := s.Put("big", "o", Result{Data: make([]byte, 11)}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	if err := s.Delete(b.ID, "o2"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete by other: %v", err)
	}
	if err := s.Delete(b.ID, "o1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(b.ID, "o1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	// TTL
	d := put(1, "o1")
	c.t = c.t.Add(time.Hour)
	if _, ok := s.Get(d.ID); ok {
		t.Fatal("expired file served")
	}
	s.mu.Lock()
	s.expireLocked(c.t)
	s.mu.Unlock()
	if used, n := s.Usage(); used != 0 || n != 0 {
		t.Fatalf("after expiry %d/%d", used, n)
	}
}

func TestTokens(t *testing.T) {
	tk := NewTokens(2, 10*time.Second)
	tok, revoke, err := tk.Issue()
	if err != nil || len(tok) != 22 {
		t.Fatalf("issue: %q %v", tok, err)
	}
	owner, ok := tk.Owner(tok)
	if !ok || owner == "" || owner == tok {
		t.Fatal("owner")
	}
	now := time.Unix(1, 0)
	a1, _ := tk.Allow(tok, now)
	a2, _ := tk.Allow(tok, now)
	a3, wait := tk.Allow(tok, now)
	if !a1 || !a2 || a3 || wait <= 0 {
		t.Fatal("rate limit")
	}
	revoke()
	revoke()
	if _, ok := tk.Owner(tok); ok {
		t.Fatal("token valid after revoke")
	}
	if ok, _ := tk.Allow(tok, now); ok {
		t.Fatal("revoked token allowed")
	}
	for k := range tk.byID {
		if string(k[:]) == tok {
			t.Fatal("raw token stored")
		}
	}
}

// --- HTTP ---------------------------------------------------------------

type fixture struct {
	h      *Handler
	store  *Store
	tokens *Tokens
	token  string
	revoke func()
	clock  *clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := &clock{time.Unix(1_000_000, 0)}
	st := NewStore(1<<20, time.Hour, c.now)
	tk := NewTokens(3, 10*time.Second)
	tok, revoke, err := tk.Issue()
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: NewHandler(HandlerOptions{Store: st, Tokens: tk, MaxUpload: 1 << 19, Now: c.now}), store: st, tokens: tk, token: tok, revoke: revoke, clock: c}
}

func (f *fixture) upload(t *testing.T, name string, body []byte, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/upload?name="+name, bytes.NewReader(body))
	req.Host = "chat.example.fr"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.h.Upload(rec, req)
	return rec
}

func (f *fixture) get(t *testing.T, method, id string, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /f/{id}/{name}", f.h.Serve)
	mux.HandleFunc("DELETE /f/{id}", f.h.Delete)
	path := "/f/" + id + "/x"
	if method == http.MethodDelete {
		path = "/f/" + id
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUploadAndServe(t *testing.T) {
	f := newFixture(t)
	rec := f.upload(t, "trip%20photo.jpeg", jpegWithEXIF(t, 6, 4, 1, "GPS-secret"), f.token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var reply UploadReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.URL != "http://chat.example.fr/f/"+reply.ID+"/trip_photo.jpg" {
		t.Fatalf("url = %q", reply.URL)
	}
	if _, err := time.Parse(time.RFC3339, reply.Expires); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, http.MethodGet, reply.ID, "")
	hdr := got.Header()
	if got.Code != 200 || hdr.Get("Content-Type") != TypeJPEG || hdr.Get("Content-Security-Policy") != FileCSP ||
		!strings.HasPrefix(hdr.Get("Content-Disposition"), "inline") || hdr.Get("Cache-Control") != "private, max-age=3600" {
		t.Fatalf("served %d %v", got.Code, hdr)
	}
	if bytes.Contains(got.Body.Bytes(), []byte("GPS-secret")) {
		t.Fatal("EXIF served")
	}
	// SVG with script: attachment, opaque type (T4).
	rec = f.upload(t, "x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), f.token)
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	got = f.get(t, http.MethodGet, reply.ID, "")
	if got.Header().Get("Content-Type") != TypeBinary || !strings.HasPrefix(got.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("svg served as %v", got.Header())
	}
	// Expired files disappear (TTL).
	f.clock.t = f.clock.t.Add(time.Hour)
	if f.get(t, http.MethodGet, reply.ID, "").Code != http.StatusNotFound {
		t.Fatal("expired file still served")
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newFixture(t)
	small := []byte("hello")
	tests := []struct {
		name  string
		do    func() *httptest.ResponseRecorder
		code  int
		retry bool
	}{
		{"no token", func() *httptest.ResponseRecorder { return f.upload(t, "a", small, "") }, 401, false},
		{"unknown token", func() *httptest.ResponseRecorder { return f.upload(t, "a", small, "forged") }, 401, false},
		{"empty", func() *httptest.ResponseRecorder { return f.upload(t, "a", nil, f.token) }, 400, false},
		{"too large", func() *httptest.ResponseRecorder { return f.upload(t, "a", make([]byte, 1<<19+1), f.token) }, 413, false},
		{"bomb", func() *httptest.ResponseRecorder { return f.upload(t, "a", pngBomb(60000, 60000), f.token) }, 413, false},
	}
	for _, tt := range tests {
		if rec := tt.do(); rec.Code != tt.code {
			t.Errorf("%s: %d %s", tt.name, rec.Code, rec.Body)
		}
	}
	// Burst of 3 per connection (the 3 refusals above consumed tokens too).
	f2 := newFixture(t)
	for i := range 3 {
		if rec := f2.upload(t, "a", small, f2.token); rec.Code != http.StatusCreated {
			t.Fatalf("upload %d = %d", i, rec.Code)
		}
	}
	rec := f2.upload(t, "a", small, f2.token)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "10" {
		t.Fatalf("4th upload = %d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Missing Content-Length.
	f3 := newFixture(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/upload", strings.NewReader("x"))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+f3.token)
	rr := httptest.NewRecorder()
	f3.h.Upload(rr, req)
	if rr.Code != http.StatusLengthRequired {
		t.Fatalf("chunked upload = %d", rr.Code)
	}
}

// T9: a token stops working when its connection ends.
func TestTokenRevokedAfterQuit(t *testing.T) {
	f := newFixture(t)
	rec := f.upload(t, "a.txt", []byte("hi"), f.token)
	var reply UploadReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	f.revoke() // the IRC connection is gone
	if rec := f.upload(t, "b.txt", []byte("hi"), f.token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload after quit = %d", rec.Code)
	}
	if rec := f.get(t, http.MethodDelete, reply.ID, f.token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("delete after quit = %d", rec.Code)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	rec := f.upload(t, "a.txt", []byte("hi"), f.token)
	var reply UploadReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	other, _, _ := f.tokens.Issue()
	if rec := f.get(t, http.MethodDelete, reply.ID, other); rec.Code != http.StatusForbidden {
		t.Fatalf("delete by other = %d", rec.Code)
	}
	if rec := f.get(t, http.MethodDelete, reply.ID, f.token); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := f.get(t, http.MethodDelete, reply.ID, f.token); rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice = %d", rec.Code)
	}
	if rec := f.get(t, http.MethodGet, reply.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted = %d", rec.Code)
	}
}

// T6: concurrent uploads from many connections never push the store over
// its budget; the oldest files make room.
func TestUploadBudgetUnderConcurrency(t *testing.T) {
	const (
		budget  = 256 << 10
		fileLen = 40 << 10
		uploads = 3
		conns   = 16
	)
	st := NewStore(budget, time.Hour, nil)
	tk := NewTokens(uploads, time.Hour)
	h := NewHandler(HandlerOptions{Store: st, Tokens: tk, MaxUpload: 64 << 10})
	var wg sync.WaitGroup
	var peak atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if used, _ := st.Usage(); used > peak.Load() {
					peak.Store(used)
				}
			}
		}
	}()
	for range conns {
		tok, _, err := tk.Issue()
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			for range uploads + 1 { // the last one hits the rate limit
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/upload?name=x.bin", bytes.NewReader(make([]byte, fileLen)))
				req.Header.Set("Authorization", "Bearer "+tok)
				h.Upload(httptest.NewRecorder(), req)
			}
		})
	}
	wg.Wait()
	close(stop)
	used, n := st.Usage()
	if peak.Load() > budget || used > budget || n != budget/fileLen {
		t.Fatalf("peak %d, used %d, %d files (budget %d)", peak.Load(), used, n, budget)
	}
}
