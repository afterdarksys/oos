package main

// Row filters shared by --scan, --audit, --by-type and --dupes: age windows,
// extension lists, sort order and a row cap. Pure functions; the modes apply
// them to their own row types.

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parseAge reads "90d", "2w", "36h", "6mo", "1y" or a bare number of days.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad age %q: want a number followed by h, d, w, mo or y", s)
	}
	unit := strings.TrimSpace(s[i:])
	day := 24 * time.Hour
	var mult time.Duration
	switch unit {
	case "h":
		mult = time.Hour
	case "", "d":
		mult = day
	case "w":
		mult = 7 * day
	case "mo":
		mult = 30 * day
	case "y":
		mult = 365 * day
	default:
		return 0, fmt.Errorf("bad age unit in %q: want h, d, w, mo or y", s)
	}
	return time.Duration(n * float64(mult)), nil
}

// rowFilter is what the flags asked for.
type rowFilter struct {
	olderThan, newerThan time.Duration
	exts                 map[string]bool
	sortBy               string // size (default) | oldest | newest | name
	top                  int    // 0 = the mode's own default
	tag                  string
}

func filterFromOpts(o *opts) (rowFilter, error) {
	f := rowFilter{sortBy: strings.ToLower(strings.TrimSpace(o.sortBy)), top: o.top, tag: strings.TrimSpace(o.tag)}
	var err error
	if f.olderThan, err = parseAge(o.olderThan); err != nil {
		return f, err
	}
	if f.newerThan, err = parseAge(o.newerThan); err != nil {
		return f, err
	}
	switch f.sortBy {
	case "", "size":
		f.sortBy = "size"
	case "oldest", "newest", "name":
	default:
		return f, fmt.Errorf("--sort %q: want size, oldest, newest or name", o.sortBy)
	}
	if strings.TrimSpace(o.ext) != "" {
		f.exts = map[string]bool{}
		for _, e := range strings.Split(o.ext, ",") {
			e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if e != "" {
				f.exts[e] = true
			}
		}
	}
	if f.top < 0 {
		return f, fmt.Errorf("--top must be >= 0")
	}
	return f, nil
}

// keepTime applies the age window to a modification time.
func (f rowFilter) keepTime(mtime, now time.Time) bool {
	age := now.Sub(mtime)
	if f.olderThan > 0 && age < f.olderThan {
		return false
	}
	if f.newerThan > 0 && age > f.newerThan {
		return false
	}
	return true
}

// keepName applies the extension list.
func (f rowFilter) keepName(name string) bool {
	if f.exts == nil {
		return true
	}
	e := extOf(name)
	if f.exts[e] {
		return true
	}
	// a compound extension answers to each of its parts: "log" and "gz" both match app.log.gz
	for _, part := range strings.Split(e, ".") {
		if f.exts[part] {
			return true
		}
	}
	return false
}

// describe says what the window and extension list were, for report headers.
func (f rowFilter) describe() string {
	var parts []string
	if f.olderThan > 0 {
		parts = append(parts, "older than "+ageString(f.olderThan))
	}
	if f.newerThan > 0 {
		parts = append(parts, "newer than "+ageString(f.newerThan))
	}
	if f.exts != nil {
		keys := make([]string, 0, len(f.exts))
		for k := range f.exts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts = append(parts, "ext "+strings.Join(keys, ","))
	}
	if f.tag != "" {
		parts = append(parts, "tag "+f.tag)
	}
	if f.sortBy != "size" {
		parts = append(parts, "sort "+f.sortBy)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func ageString(d time.Duration) string {
	days := d.Hours() / 24
	switch {
	case d < 48*time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	case days >= 365 && days/365 == float64(int(days/365)):
		return fmt.Sprintf("%dy", int(days/365))
	default:
		return fmt.Sprintf("%dd", int(days))
	}
}

// lessFor returns the comparison for a sort key given size, mtime and name
// accessors. size: largest first; oldest: earliest mtime first; newest:
// latest first; name: ascending.
func lessFor(sortBy string, size func(i int) int64, mtime func(i int) time.Time, name func(i int) string) func(i, j int) bool {
	switch sortBy {
	case "oldest":
		return func(i, j int) bool { return mtime(i).Before(mtime(j)) }
	case "newest":
		return func(i, j int) bool { return mtime(i).After(mtime(j)) }
	case "name":
		return func(i, j int) bool { return name(i) < name(j) }
	}
	return func(i, j int) bool { return size(i) > size(j) }
}

func capRows(n, top, def int) int {
	limit := def
	if top > 0 {
		limit = top
	}
	if limit > 0 && n > limit {
		return limit
	}
	return n
}

// compoundExts are the multi-part extensions that count as one.
var compoundExts = []string{"tar.gz", "tar.xz", "tar.bz2", "tar.zst", "tar.lz", "pkg.tar.zst", "sql.gz", "log.gz"}

// extOf returns the lowercase extension without the dot, honouring compound
// forms like tar.gz; "" for none. A trailing ".1"/".2" (rotated logs) is
// dropped so "syslog.log.1" is a log.
func extOf(name string) string {
	n := strings.ToLower(filepath.Base(name))
	parts := strings.Split(n, ".")
	if len(parts) < 2 || parts[0] == "" && len(parts) == 2 {
		return "" // no dot, or a dotfile like .bashrc
	}
	// drop the stem and any rotation counters ("app.log.2.gz" -> log.gz)
	var keep []string
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		if _, err := strconv.Atoi(p); err == nil && len(p) <= 2 {
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == 0 {
		return ""
	}
	joined := strings.Join(keep, ".")
	for _, c := range compoundExts {
		if joined == c || strings.HasSuffix(joined, "."+c) {
			return c
		}
	}
	return keep[len(keep)-1]
}

// fileTypes maps extensions to the categories --by-type reports.
var fileTypes = map[string]string{}

func init() {
	add := func(cat string, exts ...string) {
		for _, e := range exts {
			fileTypes[e] = cat
		}
	}
	add("disk image", "dmg", "iso", "img", "qcow2", "qcow", "vmdk", "vdi", "vhd", "vhdx", "raw", "sparseimage", "sparsebundle", "ova", "ovf", "wim")
	add("archive", "zip", "tar", "gz", "tgz", "bz2", "xz", "zst", "7z", "rar", "lz4", "lzma", "tar.gz", "tar.xz", "tar.bz2", "tar.zst", "tar.lz", "cab", "jar", "war", "aar", "whl", "egg", "gem", "crate", "nupkg")
	add("package", "deb", "rpm", "pkg", "apk", "ipa", "msi", "appimage", "snap", "flatpak", "pkg.tar.zst")
	add("video", "mp4", "mkv", "mov", "avi", "webm", "m4v", "wmv", "flv", "mpg", "mpeg", "ts", "m2ts", "prores", "mxf")
	add("audio", "mp3", "wav", "flac", "aac", "m4a", "ogg", "opus", "aiff", "aif", "wma", "alac")
	add("image", "png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tif", "tiff", "bmp", "psd", "svg", "raw", "cr2", "nef", "arw", "dng", "ai", "sketch", "fig")
	add("document", "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp", "rtf", "pages", "numbers", "key", "epub", "mobi", "txt", "md", "csv", "tsv")
	add("log", "log", "log.gz", "out", "err", "jsonl", "ndjson", "trace")
	add("database", "db", "sqlite", "sqlite3", "db3", "mdb", "accdb", "sql", "sql.gz", "dump", "bak", "rdb", "aof", "ldb", "sst", "ibd", "frm", "myd", "myi", "wal")
	add("binary", "so", "dylib", "dll", "a", "o", "lib", "exe", "bin", "wasm", "pyc", "class", "rlib", "rmeta", "node")
	add("model", "safetensors", "gguf", "ggml", "pt", "pth", "ckpt", "onnx", "h5", "pb", "tflite", "npy", "npz", "pkl", "pickle", "bin.index.json")
	add("source", "go", "rs", "py", "js", "ts", "tsx", "jsx", "c", "h", "cc", "cpp", "hpp", "java", "kt", "swift", "rb", "php", "sh", "zsh", "bash", "pl", "lua", "scala", "cs", "m", "mm", "dart", "ex", "exs", "erl", "hs", "ml", "clj", "vue", "svelte", "html", "css", "scss", "less", "json", "yaml", "yml", "toml", "xml", "proto", "graphql", "sql", "tf", "hcl", "nix", "cmake", "mk", "make", "gradle", "sbt", "cabal", "el", "vim")
	add("font", "ttf", "otf", "woff", "woff2", "eot")
}

// magicType sniffs a file whose extension said nothing. Cheap: one read of
// the first 512 bytes plus the tar magic at offset 257.
func magicType(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	if n < 4 {
		return ""
	}
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "archive"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("7z\xBC\xAF\x27\x1C")), bytes.HasPrefix(head, []byte("Rar!")), bytes.HasPrefix(head, []byte{0xFD, '7', 'z', 'X', 'Z', 0}), bytes.HasPrefix(head, []byte("BZh")), bytes.HasPrefix(head, []byte{0x28, 0xB5, 0x2F, 0xFD}):
		return "archive"
	case n > 262 && string(head[257:262]) == "ustar":
		return "archive"
	case bytes.HasPrefix(head, []byte("%PDF")):
		return "document"
	case bytes.HasPrefix(head, []byte("SQLite format 3")):
		return "database"
	case bytes.HasPrefix(head, []byte{0x7f, 'E', 'L', 'F'}), bytes.HasPrefix(head, []byte{0xCF, 0xFA, 0xED, 0xFE}), bytes.HasPrefix(head, []byte{0xCA, 0xFE, 0xBA, 0xBE}), bytes.HasPrefix(head, []byte("MZ")):
		return "binary"
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G'}), bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}), bytes.HasPrefix(head, []byte("GIF8")), bytes.HasPrefix(head, []byte("RIFF")) && n >= 12 && string(head[8:12]) == "WEBP":
		return "image"
	case n >= 12 && string(head[4:8]) == "ftyp":
		return "video"
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "video"
	case bytes.HasPrefix(head, []byte("ID3")), bytes.HasPrefix(head, []byte("fLaC")), bytes.HasPrefix(head, []byte("OggS")), bytes.HasPrefix(head, []byte("RIFF")) && n >= 12 && string(head[8:12]) == "WAVE":
		return "audio"
	case bytes.HasPrefix(head, []byte("koly")):
		return "disk image"
	}
	// text vs binary: a NUL in the first 512 bytes means binary
	if bytes.IndexByte(head, 0) >= 0 {
		return "binary"
	}
	sc := bufio.NewScanner(bytes.NewReader(head))
	if sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#!") {
			return "source"
		}
		if len(line) > 0 && (line[0] == '{' || line[0] == '[') {
			return "source"
		}
	}
	return "text"
}

// typeOf classifies a file by extension, then by magic when the extension
// says nothing and the file is big enough to matter.
func typeOf(path string, size int64) string {
	e := extOf(path)
	if e != "" {
		if t, ok := fileTypes[e]; ok {
			return t
		}
	}
	if size >= 1<<20 {
		if t := magicType(path); t != "" {
			return t
		}
	}
	if e == "" {
		return "no extension"
	}
	return "other (." + e + ")"
}

// TypeRow is one category in a --by-type report.
type TypeRow struct {
	Type    string    `json:"type"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	Largest []BigFile `json:"largest"`
}

// byType walks root and buckets every regular file by category. Same
// symlink and device rules as scanBig; the filter's age and extension
// windows apply per file.
func byType(root string, f rowFilter, now time.Time, keepLargest int) ([]TypeRow, int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory", root)
	}
	rootDev, haveDev := deviceOf(info)
	rows := map[string]*TypeRow{}
	var total int64
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if haveDev {
				if dev, ok := deviceOf(fi); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !fi.Mode().IsRegular() || !f.keepTime(fi.ModTime(), now) || !f.keepName(p) {
			return nil
		}
		b := allocated(fi)
		t := typeOf(p, b)
		r := rows[t]
		if r == nil {
			r = &TypeRow{Type: t}
			rows[t] = r
		}
		r.Files++
		r.Bytes += b
		total += b
		r.Largest = insertLargest(r.Largest, BigFile{Path: p, Bytes: b, ModTime: fi.ModTime()}, keepLargest)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]TypeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Type < out[j].Type
	})
	return out, total, nil
}

// insertLargest keeps the k biggest files, largest first.
func insertLargest(list []BigFile, b BigFile, k int) []BigFile {
	if k <= 0 {
		return nil
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].Bytes < b.Bytes })
	if i >= k {
		return list
	}
	list = append(list, BigFile{})
	copy(list[i+1:], list[i:])
	list[i] = b
	if len(list) > k {
		list = list[:k]
	}
	return list
}
