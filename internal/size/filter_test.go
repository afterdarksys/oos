package size

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	cases := map[string]time.Duration{
		"90d": 90 * day, "2w": 14 * day, "36h": 36 * time.Hour, "6mo": 180 * day, "1y": 365 * day,
		"30": 30 * day, "": 0, " 1.5d ": 36 * time.Hour, "0d": 0,
	}
	for in, want := range cases {
		got, err := ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"x", "3 fortnights", "-1d", "1s", "d"} {
		if _, err := ParseAge(bad); err == nil {
			t.Errorf("ParseAge(%q) should fail", bad)
		}
	}
}

func TestExtOfAndTypeOf(t *testing.T) {
	cases := map[string]string{
		"a.tar.gz": "tar.gz", "b.TAR.XZ": "tar.xz", "syslog.log.1": "log", "app.log.2.gz": "log.gz",
		"x.dmg": "dmg", "README": "", ".bashrc": "", "dir.v2/file.ISO": "iso", "noext.": "",
	}
	for in, want := range cases {
		if got := ExtOf(in); got != want {
			t.Errorf("ExtOf(%q) = %q want %q", in, got, want)
		}
	}
	types := map[string]string{
		"x.dmg": "disk image", "y.tar.gz": "archive", "z.mkv": "video", "w.log.1": "log",
		"v.sqlite3": "database", "u.go": "source", "t.deb": "package", "s.safetensors": "model",
		"r.xyz": "other (.xyz)", "README": "no extension",
	}
	for in, want := range types {
		if got := TypeOf(in, 0); got != want {
			t.Errorf("TypeOf(%q) = %q want %q", in, got, want)
		}
	}
}

func TestMagicType(t *testing.T) {
	home := t.TempDir()
	put := func(name string, head []byte) string {
		p := filepath.Join(home, name)
		b := append(append([]byte{}, head...), bytes.Repeat([]byte{'x'}, 1<<20)...)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"gz":     "archive",
		"pdf":    "document",
		"sqlite": "database",
		"elf":    "binary",
		"png":    "image",
		"text":   "text",
		"json":   "source",
	}
	heads := map[string][]byte{
		"gz": {0x1f, 0x8b, 8, 0}, "pdf": []byte("%PDF-1.7"), "sqlite": []byte("SQLite format 3\x00"),
		"elf": {0x7f, 'E', 'L', 'F'}, "png": {0x89, 'P', 'N', 'G'}, "text": []byte("hello there\n"), "json": []byte("{\"a\":1}\n"),
	}
	for name, want := range cases {
		p := put("blob-"+name, heads[name]) // no extension on purpose
		if got := TypeOf(p, 2<<20); got != want {
			t.Errorf("%s: typeOf = %q want %q", name, got, want)
		}
	}
	// a strong signature classifies an extensionless file of any size
	if got := TypeOf(put("tiny", []byte("%PDF")), 4); got != "document" {
		t.Errorf("tiny pdf: %q", got)
	}
	jpg := put("notes.jpg", []byte("%PDF-1.7"))
	if got := TypeOf(jpg, 2<<20); got != "document" {
		t.Errorf("a large file whose extension lies: %q", got)
	}
	if got := TypeOf(jpg, 4); got != "image" {
		t.Errorf("a small file keeps its extension, got %q", got)
	}
	heic := put("noext-heic", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'})
	if got := TypeOf(heic, 2<<20); got != "image" {
		t.Errorf("heic ftyp: %q", got)
	}
	mp4 := put("noext-mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'})
	if got := TypeOf(mp4, 2<<20); got != "video" {
		t.Errorf("mp4 ftyp: %q", got)
	}
	if got := TypeOf(put("hello.txt", []byte{0x89, 'P', 'N', 'G'}), 32); got != "document" {
		t.Errorf("small txt stays a document by extension, got %q", got)
	}
}
