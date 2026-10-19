// Package files keeps uploaded files in RAM (TTL, global budget, eviction),
// sanitizes images by decoding and re-encoding them, and serves them.
package files

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"

	"golang.org/x/image/webp"
)

// Image limits (SPEC §6.3, THREAT_MODEL T5).
const (
	MaxPixels = 25_000_000
	// MaxGIFFrames and MaxGIFTotalPixels bound an animation before
	// gif.DecodeAll allocates every frame.
	MaxGIFFrames      = 1000
	MaxGIFTotalPixels = 100_000_000
	jpegQuality       = 90
)

// Content types.
const (
	TypeJPEG   = "image/jpeg"
	TypePNG    = "image/png"
	TypeGIF    = "image/gif"
	TypeBinary = "application/octet-stream"
	typeWebP   = "image/webp"
)

// Errors returned by Sanitize.
var (
	ErrTooManyPixels = errors.New("files: image too large")
	ErrBadImage      = errors.New("files: invalid image")
)

// Result is a sanitized upload.
type Result struct {
	Data  []byte
	Type  string // served Content-Type
	Ext   string // file extension matching Type, "" for binaries
	Image bool   // re-encoded image, safe to display inline
}

// Sanitize sniffs data by content (never by name or client header).
// Images are size-checked with DecodeConfig, decoded, and re-encoded, which
// drops EXIF/GPS and comments; WebP becomes PNG. Anything else is returned
// as an opaque binary, to be served as an attachment.
func Sanitize(data []byte) (Result, error) {
	switch http.DetectContentType(data) {
	case TypeJPEG:
		return still(data, jpeg.DecodeConfig, jpeg.Decode, func(img image.Image) image.Image {
			return orient(img, jpegOrientation(data))
		}, encodeJPEG, TypeJPEG, ".jpg")
	case TypePNG:
		return still(data, png.DecodeConfig, png.Decode, nil, encodePNG, TypePNG, ".png")
	case typeWebP:
		return still(data, webp.DecodeConfig, webp.Decode, nil, encodePNG, TypePNG, ".png")
	case TypeGIF:
		return animated(data)
	default:
		return Result{Data: data, Type: TypeBinary}, nil
	}
}

type (
	configFunc func(io.Reader) (image.Config, error)
	decodeFunc func(io.Reader) (image.Image, error)
	encodeFunc func(img image.Image) ([]byte, error)
)

func checkPixels(w, h int) error {
	if w <= 0 || h <= 0 {
		return ErrBadImage
	}
	if int64(w)*int64(h) > MaxPixels {
		return ErrTooManyPixels
	}
	return nil
}

func still(data []byte, cfgFn configFunc, decFn decodeFunc, fix func(image.Image) image.Image,
	enc encodeFunc, typ, ext string,
) (Result, error) {
	cfg, err := cfgFn(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrBadImage
	}
	if err := checkPixels(cfg.Width, cfg.Height); err != nil {
		return Result{}, err
	}
	img, err := decFn(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrBadImage
	}
	if fix != nil {
		img = fix(img)
	}
	out, err := enc(img)
	if err != nil {
		return Result{}, fmt.Errorf("files: encode: %w", err)
	}
	return Result{Data: out, Type: typ, Ext: ext, Image: true}, nil
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("jpeg: %w", err)
	}
	return b.Bytes(), nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, fmt.Errorf("png: %w", err)
	}
	return b.Bytes(), nil
}

// animated re-encodes a GIF, keeping its frames, delays and loop count but
// no comment or application extension.
func animated(data []byte) (Result, error) {
	cfg, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrBadImage
	}
	if err := checkPixels(cfg.Width, cfg.Height); err != nil {
		return Result{}, err
	}
	if err := checkGIFFrames(data); err != nil {
		return Result{}, err
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return Result{}, ErrBadImage
	}
	clean := &gif.GIF{Image: g.Image, Delay: g.Delay, Disposal: g.Disposal, LoopCount: g.LoopCount, Config: g.Config, BackgroundIndex: g.BackgroundIndex}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, clean); err != nil {
		return Result{}, fmt.Errorf("files: encode gif: %w", err)
	}
	return Result{Data: b.Bytes(), Type: TypeGIF, Ext: ".gif", Image: true}, nil
}

// checkGIFFrames walks the GIF block structure (no pixel is decoded) and
// bounds the number of frames and their cumulated area.
func checkGIFFrames(data []byte) error {
	const headerLen = 13 // signature, logical screen descriptor
	if len(data) < headerLen {
		return ErrBadImage
	}
	p := headerLen
	if flags := data[10]; flags&0x80 != 0 { // global color table
		p += 3 << (int(flags&0x07) + 1)
	}
	frames, area := 0, int64(0)
	for p < len(data) {
		switch data[p] {
		case 0x3B: // trailer
			return nil
		case 0x21: // extension: label, then sub-blocks
			if p+2 > len(data) {
				return ErrBadImage
			}
			var ok bool
			if p, ok = skipSubBlocks(data, p+2); !ok {
				return ErrBadImage
			}
		case 0x2C: // image descriptor
			if p+10 > len(data) {
				return ErrBadImage
			}
			w := int64(data[p+5]) | int64(data[p+6])<<8
			h := int64(data[p+7]) | int64(data[p+8])<<8
			flags := data[p+9]
			frames++
			area += w * h
			if frames > MaxGIFFrames || area > MaxGIFTotalPixels {
				return ErrTooManyPixels
			}
			p += 10
			if flags&0x80 != 0 { // local color table
				p += 3 << (int(flags&0x07) + 1)
			}
			p++ // LZW minimum code size
			var ok bool
			if p, ok = skipSubBlocks(data, p); !ok {
				return ErrBadImage
			}
		default:
			return ErrBadImage
		}
	}
	return nil // truncated after the last frame: DecodeAll decides
}

// skipSubBlocks returns the offset after a sub-block chain starting at p.
func skipSubBlocks(data []byte, p int) (int, bool) {
	for p < len(data) {
		n := int(data[p])
		p++
		if n == 0 {
			return p, true
		}
		p += n
	}
	return p, false
}
