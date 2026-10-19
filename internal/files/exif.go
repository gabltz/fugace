package files

import (
	"encoding/binary"
	"image"
)

// EXIF orientation values that need a transform (1 = as stored).
const (
	orientNormal    = 1
	orientMax       = 8
	exifOrientation = 0x0112
	jpegAPP1        = 0xE1
	jpegSOS         = 0xDA
	maxIFDEntries   = 512
)

// jpegOrientation reads only the EXIF Orientation tag of a JPEG (APP1).
// Anything unexpected yields orientNormal: re-encoding drops EXIF anyway,
// this just keeps phone photos upright.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return orientNormal
	}
	p := 2
	for p+4 <= len(data) {
		if data[p] != 0xFF {
			return orientNormal
		}
		marker := data[p+1]
		if marker == jpegSOS {
			return orientNormal // image data starts: no APP1 found
		}
		size := int(binary.BigEndian.Uint16(data[p+2:]))
		if size < 2 || p+2+size > len(data) {
			return orientNormal
		}
		seg := data[p+4 : p+2+size]
		if marker == jpegAPP1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		p += 2 + size
	}
	return orientNormal
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return orientNormal
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return orientNormal
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return orientNormal
	}
	n := int(bo.Uint16(t[ifd:]))
	for i := range min(n, maxIFDEntries) {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			return orientNormal
		}
		if bo.Uint16(t[e:]) == exifOrientation {
			if v := int(bo.Uint16(t[e+8:])); v >= orientNormal && v <= orientMax {
				return v
			}
			return orientNormal
		}
	}
	return orientNormal
}

// orient applies an EXIF orientation (2-8) to img.
func orient(img image.Image, o int) image.Image {
	if o <= orientNormal || o > orientMax {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5 // 5-8 transpose width and height
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	// Written straight into the destination: no intermediate full copy.
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 counter-clockwise
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
