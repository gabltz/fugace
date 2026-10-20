package files

import (
	"bytes"
	"errors"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestMakeAvatar(t *testing.T) {
	pic, err := MakeAvatar(jpegWithEXIF(t, 60, 30, 1, "GPS-secret"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(pic))
	if err != nil || img.Bounds().Dx() != AvatarSize || img.Bounds().Dy() != AvatarSize {
		t.Fatalf("picture %v, %v", img.Bounds(), err)
	}
	if bytes.Contains(pic, []byte("GPS-secret")) || bytes.Contains(pic, []byte("Exif")) {
		t.Fatal("metadata kept in the picture")
	}
	if _, err := MakeAvatar([]byte("<svg onload=alert(1)>")); !errors.Is(err, errNotAnImage) {
		t.Fatalf("svg accepted: %v", err)
	}
	if _, err := MakeAvatar(pngBomb(50000, 50000)); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("bomb: %v", err)
	}
}

func avatarFixture(t *testing.T) (*AvatarHandler, *Avatars, string, string, *[]string) {
	t.Helper()
	f := newFixture(t)
	av := NewAvatars()
	owner, _ := f.tokens.Owner(f.token)
	var changed []string
	h := NewAvatarHandler(f.h, av, func(nick string) (string, bool) { return owner, nick == "alice" }, func(o string) { changed = append(changed, o) })
	return h, av, f.token, owner, &changed
}

func TestAvatarHandlers(t *testing.T) {
	h, av, token, owner, changed := avatarFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /avatar", h.Upload)
	mux.HandleFunc("DELETE /avatar", h.Delete)
	mux.HandleFunc("GET /avatar/{nick}", h.Serve)
	do := func(method, path, tok string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodPost, "/avatar", "", jpegWithEXIF(t, 10, 10, 1, "x")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous upload = %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/avatar", token, []byte("not an image")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image = %d", rec.Code)
	}
	rec := do(http.MethodPost, "/avatar", token, jpegWithEXIF(t, 40, 20, 1, "x"))
	if rec.Code != http.StatusCreated || av.Version(owner) == 0 || len(*changed) != 1 || (*changed)[0] != owner {
		t.Fatalf("upload = %d %s, changed %v", rec.Code, rec.Body, *changed)
	}
	v := strconv.Itoa(av.Version(owner))
	// Only connected users (valid token in the URL) can see pictures.
	if rec := do(http.MethodGet, "/avatar/alice", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous view = %d", rec.Code)
	}
	rec = do(http.MethodGet, "/avatar/alice?t="+token+"&v="+v, "", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != TypeJPEG || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") ||
		rec.Header().Get("Content-Security-Policy") != FileCSP {
		t.Fatalf("view = %d %v", rec.Code, rec.Header())
	}
	if rec := do(http.MethodGet, "/avatar/alice?t="+token+"&v=old", "", nil); !strings.Contains(rec.Header().Get("Cache-Control"), "no-cache") {
		t.Fatal("stale version cached")
	}
	if rec := do(http.MethodGet, "/avatar/ghost?t="+token, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown nick = %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/avatar", token, nil); rec.Code != http.StatusNoContent || av.Version(owner) != 0 || len(*changed) != 2 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/avatar/alice?t="+token, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted picture served: %d", rec.Code)
	}
}

func TestDeleteOwnerAndOwnerTokens(t *testing.T) {
	st := NewStore(1<<20, 3600e9, nil)
	tk := NewTokens(10, 1e9)
	t1, _, _ := tk.Issue("k-owner")
	t2, _, _ := tk.Issue("k-owner")
	o1, _ := tk.Owner(t1)
	o2, _ := tk.Owner(t2)
	if o1 != "k-owner" || o2 != o1 {
		t.Fatal("tokens of one identity must share the owner")
	}
	for _, o := range []string{"k-owner", "k-owner", "other"} {
		if _, err := st.Put("f", o, Result{Data: []byte("x"), Type: TypeBinary}); err != nil {
			t.Fatal(err)
		}
	}
	if n := st.DeleteOwner("k-owner"); n != 2 {
		t.Fatalf("deleted %d", n)
	}
	if _, n := st.Usage(); n != 1 {
		t.Fatalf("%d files left", n)
	}
	av := NewAvatars()
	if _, err := av.set("o", []byte("x")); err != nil || av.Len() != 1 {
		t.Fatal(err)
	}
	av.Delete("o")
	if av.Len() != 0 {
		t.Fatal("avatar kept")
	}
}

func FuzzMakeAvatar(f *testing.F) {
	f.Add(jpegWithEXIF(&testing.T{}, 9, 4, 6, "x"))
	f.Add(animatedGIF(&testing.T{}, 2, ""))
	f.Add([]byte("<svg/>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		pic, err := MakeAvatar(data)
		if err != nil {
			return
		}
		img, err := jpeg.Decode(bytes.NewReader(pic))
		if err != nil || img.Bounds().Dx() != AvatarSize || img.Bounds().Dy() != AvatarSize {
			t.Fatalf("bad picture: %v", err)
		}
	})
}
