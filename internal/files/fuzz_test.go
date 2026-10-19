package files

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	_ "golang.org/x/image/webp" // register for image.DecodeConfig
)

// fuzzMaxPixels keeps fuzzing fast: valid huge images only cost RAM, the
// 25 MP cap itself is covered by unit tests.
const fuzzMaxPixels = 1 << 20

// FuzzSanitize: no panic, and whatever is accepted as an image is a
// decodable image of the announced type, without EXIF.
func FuzzSanitize(f *testing.F) {
	webpSeed, _ := base64.StdEncoding.DecodeString(tinyWebP)
	seeds := [][]byte{
		jpegWithEXIF(&testing.T{}, 6, 4, 6, "gps"), pngWithText(&testing.T{}, "secret"),
		animatedGIF(&testing.T{}, 3, "comment"), webpSeed, pngBomb(50000, 50000),
		[]byte("GIF89a<script>"), []byte("<svg onload=alert(1)>"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && int64(cfg.Width)*int64(cfg.Height) > fuzzMaxPixels {
			return
		}
		if bytes.HasPrefix(data, []byte("GIF")) && checkGIFFrames(data) == nil && len(data) > 1<<16 {
			return
		}
		res, err := Sanitize(data)
		if err != nil {
			if !errors.Is(err, ErrBadImage) && !errors.Is(err, ErrTooManyPixels) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		if !res.Image {
			if res.Type != TypeBinary || res.Ext != "" || !bytes.Equal(res.Data, data) {
				t.Fatal("binary altered or mislabeled")
			}
			return
		}
		var derr error
		switch res.Type {
		case TypeJPEG:
			_, derr = jpeg.Decode(bytes.NewReader(res.Data))
		case TypePNG:
			_, derr = png.Decode(bytes.NewReader(res.Data))
		case TypeGIF:
			_, derr = gif.DecodeAll(bytes.NewReader(res.Data))
		default:
			t.Fatalf("unexpected type %q", res.Type)
		}
		if derr != nil {
			t.Fatalf("re-encoded %s does not decode: %v", res.Type, derr)
		}
		if bytes.Contains(res.Data, []byte("Exif\x00\x00")) {
			t.Fatal("EXIF in output")
		}
	})
}

func FuzzGIFFrames(f *testing.F) {
	f.Add(animatedGIF(&testing.T{}, 2, "c"))
	f.Add([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff\x2c\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02\x44\x01\x00\x3b"))
	f.Fuzz(func(t *testing.T, data []byte) {
		err := checkGIFFrames(data)
		if err != nil && !errors.Is(err, ErrBadImage) && !errors.Is(err, ErrTooManyPixels) {
			t.Fatal(err)
		}
	})
}

func FuzzJPEGOrientation(f *testing.F) {
	f.Add(jpegWithEXIF(&testing.T{}, 2, 2, 6, "x"))
	f.Add([]byte("\xff\xd8\xff\xe1\x00\x10Exif\x00\x00MM\x00*\x00\x00\x00\x08"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if o := jpegOrientation(data); o < orientNormal || o > orientMax {
			t.Fatalf("orientation %d", o)
		}
	})
}

func FuzzSanitizeName(f *testing.F) {
	for _, s := range []string{"photo.jpg", "../../x", "a\x00b", "été.png", strings.Repeat("z", 300)} {
		f.Add(s, ".png")
		f.Add(s, "")
	}
	f.Fuzz(func(t *testing.T, name, ext string) {
		if ext != "" && ext != ".jpg" && ext != ".png" && ext != ".gif" {
			return // only these extensions are ever passed
		}
		got := SanitizeName(name, ext)
		if got == "" || len(got) > maxNameLen || strings.ContainsAny(got, "/\\\x00\r\n\" ") || strings.HasPrefix(got, ".") {
			t.Fatalf("SanitizeName(%q, %q) = %q", name, ext, got)
		}
		if ext != "" && !strings.HasSuffix(got, ext) {
			t.Fatalf("extension lost: %q", got)
		}
	})
}
