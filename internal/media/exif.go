// Package media reads the capture time and camera from a picture. It looks
// at JPEG EXIF, a PNG eXIf chunk, and an Exif block in the first 256 KB
// (HEIC). The result is a label for reports. It is never an input to a
// delete decision, and a missing block is not an error.
package media

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"time"
)

const readLimit = 256 << 10

// Info is what a picture recorded about itself.
type Info struct {
	Taken  time.Time
	Raw    string // the EXIF date string when it did not parse
	Device string // "Make Model"
}

// TakenString is the capture time for a report line.
func (i Info) TakenString() string {
	if !i.Taken.IsZero() {
		return i.Taken.Format("2006-01-02 15:04:05")
	}
	return i.Raw
}

// Read opens path and returns Info when the file carries EXIF. ok is false
// when there is nothing to say.
func Read(path string) (Info, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, false
	}
	defer f.Close()
	buf := make([]byte, readLimit)
	n, _ := f.Read(buf)
	return parse(buf[:n])
}

func parse(b []byte) (Info, bool) {
	var tiff []byte
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8:
		tiff = jpegTIFF(b)
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		tiff = pngTIFF(b)
	default:
		tiff = findExif(b)
	}
	if tiff == nil {
		tiff = findExif(b)
	}
	if tiff == nil {
		return Info{}, false
	}
	return parseTIFF(tiff)
}

func jpegTIFF(b []byte) []byte {
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil
		}
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			return nil
		}
		marker := b[i]
		i++
		if marker == 0xD9 || marker == 0xDA {
			return nil
		}
		if i+2 > len(b) {
			return nil
		}
		seglen := int(b[i])<<8 | int(b[i+1])
		if seglen < 2 || i+seglen > len(b) {
			return nil
		}
		payload := b[i+2 : i+seglen]
		if marker == 0xE1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) && len(payload) > 6 {
			return payload[6:]
		}
		i += seglen
	}
	return nil
}

func pngTIFF(b []byte) []byte {
	i := 8
	for i+12 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[i:]))
		kind := string(b[i+4 : i+8])
		if n < 0 || i+12+n > len(b) {
			return nil
		}
		if kind == "eXIf" {
			return b[i+8 : i+8+n]
		}
		if kind == "IEND" {
			return nil
		}
		i += 12 + n
	}
	return nil
}

func findExif(b []byte) []byte {
	i := bytes.Index(b, []byte("Exif\x00\x00"))
	if i < 0 || i+6 >= len(b) {
		return nil
	}
	return b[i+6:]
}

func parseTIFF(b []byte) (Info, bool) {
	if len(b) < 8 {
		return Info{}, false
	}
	var order binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return Info{}, false
	}
	if order.Uint16(b[2:]) != 42 {
		return Info{}, false
	}
	var makeS, model, when, original, digitized string
	seen := map[int]bool{}
	var walk func(int)
	walk = func(off int) {
		if off <= 0 || seen[off] || len(seen) > 4 {
			return
		}
		seen[off] = true
		entries, _ := ifd(b, off, order)
		for _, e := range entries {
			val := e.bytes(b, order)
			switch e.tag {
			case 0x010F:
				makeS = cstr(val)
			case 0x0110:
				model = cstr(val)
			case 0x0132:
				when = cstr(val)
			case 0x9003:
				original = cstr(val)
			case 0x9004:
				digitized = cstr(val)
			case 0x8769:
				if len(val) >= 4 {
					walk(int(order.Uint32(val[:4])))
				}
			}
		}
	}
	walk(int(order.Uint32(b[4:])))
	raw := original
	if raw == "" {
		raw = digitized
	}
	if raw == "" {
		raw = when
	}
	device := strings.TrimSpace(makeS + " " + model)
	info := Info{Device: device}
	if raw != "" {
		if t, err := time.ParseInLocation("2006:01:02 15:04:05", raw, time.Local); err == nil {
			info.Taken = t
		} else {
			info.Raw = raw
		}
	}
	if info.Taken.IsZero() && info.Raw == "" && info.Device == "" {
		return Info{}, false
	}
	return info, true
}

type entry struct {
	tag   uint16
	typ   uint16
	count uint32
	raw   []byte // the 4-byte value or offset
}

func (e entry) bytes(b []byte, order binary.ByteOrder) []byte {
	n := int(e.count) * typeSize(e.typ)
	if n <= 0 || n > len(b) {
		return nil
	}
	if n <= 4 {
		if n > len(e.raw) {
			return nil
		}
		return e.raw[:n]
	}
	if len(e.raw) < 4 {
		return nil
	}
	off := int(order.Uint32(e.raw))
	if off < 0 || off > len(b) || n > len(b)-off {
		return nil
	}
	return b[off : off+n]
}

func typeSize(t uint16) int {
	switch t {
	case 1, 2, 6, 7: // byte, ascii, signed byte, undefined
		return 1
	case 3, 8:
		return 2
	case 4, 9:
		return 4
	case 5, 10:
		return 8
	default:
		return 1
	}
}

func ifd(b []byte, off int, order binary.ByteOrder) ([]entry, int) {
	if off < 0 || off+2 > len(b) {
		return nil, 0
	}
	n := int(order.Uint16(b[off:]))
	if n < 0 || n > 1024 {
		return nil, 0
	}
	body := off + 2
	need := n * 12
	if need < 0 || body > len(b) || need > len(b)-body {
		return nil, 0
	}
	out := make([]entry, 0, n)
	for i := 0; i < n; i++ {
		p := body + i*12
		out = append(out, entry{
			tag:   order.Uint16(b[p:]),
			typ:   order.Uint16(b[p+2:]),
			count: order.Uint32(b[p+4:]),
			raw:   append([]byte(nil), b[p+8:p+12]...),
		})
	}
	nextAt := body + need
	next := 0
	if nextAt+4 <= len(b) {
		next = int(order.Uint32(b[nextAt:]))
	}
	return out, next
}

func cstr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}
