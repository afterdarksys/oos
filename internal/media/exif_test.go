package media

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJPEGExifTakenAndDevice(t *testing.T) {
	tiff := buildTIFF(t)
	// APP1 payload is "Exif\0\0" + tiff. Segment length includes the two length bytes.
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := make([]byte, 2+2+len(payload))
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:], uint16(2+len(payload)))
	copy(seg[4:], payload)
	jpeg := append([]byte{0xFF, 0xD8}, seg...)
	jpeg = append(jpeg, 0xFF, 0xD9)

	dir := t.TempDir()
	p := filepath.Join(dir, "shot.jpg")
	if err := os.WriteFile(p, jpeg, 0o644); err != nil {
		t.Fatal(err)
	}
	info, ok := Read(p)
	if !ok {
		t.Fatal("expected exif")
	}
	if info.Device != "Apple iPhone 14 Pro" {
		t.Errorf("device %q", info.Device)
	}
	if info.Taken.Year() != 2022 || info.Taken.Month() != time.June || info.Taken.Day() != 14 || info.Taken.Hour() != 14 || info.Taken.Minute() != 2 {
		t.Errorf("taken %v", info.Taken)
	}
	if info.TakenString() != "2022-06-14 14:02:00" {
		t.Errorf("string %q", info.TakenString())
	}
	if _, ok := Read(filepath.Join(dir, "missing")); ok {
		t.Fatal("missing file")
	}
	plain := filepath.Join(dir, "plain.jpg")
	if err := os.WriteFile(plain, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(plain); ok {
		t.Fatal("a jpeg without exif should say nothing")
	}
}

// buildTIFF is a little-endian TIFF with Make, Model and an Exif IFD whose
// DateTimeOriginal is 2022:06:14 14:02:00.
func buildTIFF(t *testing.T) []byte {
	t.Helper()
	makeS := "Apple\x00"
	model := "iPhone 14 Pro\x00"
	when := "2022:06:14 14:02:00\x00"
	// layout: header 8, IFD0 (2 + 3*12 + 4), strings, Exif IFD (2 + 1*12 + 4), date
	ifd0 := 8
	n0 := 3
	after0 := ifd0 + 2 + n0*12 + 4
	makeAt := after0
	modelAt := makeAt + len(makeS)
	exifAt := modelAt + len(model)
	dateAt := exifAt + 2 + 12 + 4
	b := make([]byte, dateAt+len(when))
	copy(b[0:], "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], uint32(ifd0))
	binary.LittleEndian.PutUint16(b[ifd0:], uint16(n0))
	put := func(i, tag, typ int, count, val uint32) {
		p := ifd0 + 2 + i*12
		binary.LittleEndian.PutUint16(b[p:], uint16(tag))
		binary.LittleEndian.PutUint16(b[p+2:], uint16(typ))
		binary.LittleEndian.PutUint32(b[p+4:], count)
		binary.LittleEndian.PutUint32(b[p+8:], val)
	}
	put(0, 0x010F, 2, uint32(len(makeS)), uint32(makeAt))
	put(1, 0x0110, 2, uint32(len(model)), uint32(modelAt))
	put(2, 0x8769, 4, 1, uint32(exifAt))
	copy(b[makeAt:], makeS)
	copy(b[modelAt:], model)
	binary.LittleEndian.PutUint16(b[exifAt:], 1)
	p := exifAt + 2
	binary.LittleEndian.PutUint16(b[p:], 0x9003)
	binary.LittleEndian.PutUint16(b[p+2:], 2)
	binary.LittleEndian.PutUint32(b[p+4:], uint32(len(when)))
	binary.LittleEndian.PutUint32(b[p+8:], uint32(dateAt))
	copy(b[dateAt:], when)
	return b
}
