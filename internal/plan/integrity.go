package plan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/afterdarksys/oos/internal/worklimit"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/afterdarksys/oos/internal/safefs"
)

// Identity records the original object; rename preserves these attributes.
// Device numbers may change after remounting: that is reported as uncertain,
// never silently accepted as proof that the original file was recovered.
type Identity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Links  uint64 `json:"links"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime_ns"`
}

func identify(path string) (*Identity, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("file identity unavailable")
	}
	return &Identity{uint64(st.Dev), uint64(st.Ino), uint32(fi.Mode()), st.Uid, st.Gid, uint64(st.Nlink), fi.Size(), fi.ModTime().UnixNano()}, nil
}
func (id *Identity) matches(path string) bool {
	other, err := identify(path)
	return err == nil && *id == *other
}
func operationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func manifestDigest(m Manifest) string {
	m.Checksum = ""
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// treeDigest hashes relative names, metadata, symlink text and regular file
// contents. It never follows symlinks or hashes special files. It is opt-in.
func treeDigest(path string) (string, error) { return treeDigestContext(context.Background(), path) }
func treeDigestContext(ctx context.Context, path string) (string, error) {
	if _, err := safefs.MeasureContext(ctx, path); err != nil {
		return "", err
	}
	h := sha256.New()
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if e := worklimit.Step(ctx); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q:%d:%d:%d\n", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		if fi.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil {
				return e
			}
			fmt.Fprintf(h, "link:%q\n", target)
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("cannot hash special file %s", p)
		}
		parent, err := safefs.OpenDir(filepath.Dir(p))
		if err != nil {
			return err
		}
		defer parent.Close()
		f, err := parent.OpenFile(filepath.Base(p), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		before, err := f.Stat()
		if err != nil || !os.SameFile(fi, before) {
			return fmt.Errorf("file changed while hashing %s", p)
		}
		if err := worklimit.Read(ctx, fi.Size()); err != nil {
			return err
		}
		n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, fi.Size()+1))
		if err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if n != fi.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("file changed while hashing %s", p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type Verification struct {
	Batch   string              `json:"batch"`
	Version int                 `json:"version"`
	Issues  []string            `json:"issues"`
	Entries []EntryVerification `json:"entries"`
}
type EntryVerification struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func verifyEntry(e QEntry, deep bool) EntryVerification {
	return verifyEntryContext(context.Background(), e, deep)
}
func verifyEntryContext(ctx context.Context, e QEntry, deep bool) EntryVerification {
	v := EntryVerification{From: e.From, To: e.To}
	_, srcErr := os.Lstat(e.From)
	_, dstErr := os.Lstat(e.To)
	switch {
	case srcErr == nil && dstErr == nil:
		v.Status = "conflict"
		v.Detail = "both source and quarantine destination exist"
	case dstErr == nil:
		if e.Identity != nil && !e.Identity.matches(e.To) {
			v.Status = "changed"
			v.Detail = "quarantined object identity or metadata differs"
			return v
		}
		v.Status = "quarantined"
		if e.Pending {
			v.Status = "pending-moved"
		}
		if deep {
			if e.SHA256 == "" {
				v.Detail = "no recorded content hash"
			} else if hash, err := treeDigestContext(ctx, e.To); err != nil || hash != e.SHA256 {
				v.Status = "changed"
				v.Detail = fmt.Sprintf("content hash mismatch or read failure: %v", err)
			}
		}
	case srcErr == nil && os.IsNotExist(dstErr):
		if e.Identity == nil || !e.Identity.matches(e.From) {
			v.Status = "ambiguous"
			v.Detail = "source exists but original identity is not proven"
		} else {
			v.Status = "source-intact"
		}
	case os.IsNotExist(srcErr) && os.IsNotExist(dstErr):
		v.Status = "missing"
		v.Detail = "neither location exists"
	default:
		v.Status = "unreadable"
		v.Detail = fmt.Sprintf("source: %v; destination: %v", srcErr, dstErr)
	}
	if e.Identity == nil && v.Detail == "" {
		v.Detail = "legacy manifest: identity not recorded"
	}
	return v
}

// purgeBlocker reports why an entry must hold its batch back from purge, or
// "" when only the quarantined object matters and it is intact. A recreated
// source path ("conflict" at restore time) does not block expiry: apps
// recreate their caches at once, and holding the batch would fill the disk.
func purgeBlocker(e QEntry) string {
	if e.Pending {
		return "pending journal entry"
	}
	if _, err := os.Lstat(e.To); err != nil {
		if os.IsNotExist(err) {
			return "quarantined object missing"
		}
		return "quarantined object unreadable: " + err.Error()
	}
	if e.Identity != nil && !e.Identity.matches(e.To) {
		return "quarantined object identity or metadata differs"
	}
	return ""
}

func VerifyBatch(dir, name string, deep bool) (Verification, error) {
	return VerifyBatchContext(context.Background(), dir, name, deep)
}
func VerifyBatchContext(ctx context.Context, dir, name string, deep bool) (Verification, error) {
	v := Verification{Batch: name, Issues: []string{}, Entries: []EntryVerification{}}
	m, err := readManifest(dir, name)
	if err != nil {
		return v, err
	}
	v.Version = m.Version
	q := &Quarantine{Dir: dir, Batch: name, man: *m}
	if err := q.checkContentsContext(ctx); err != nil {
		v.Issues = append(v.Issues, err.Error())
	}
	for _, e := range m.Entries {
		if err := worklimit.Step(ctx); err != nil {
			return v, err
		}
		if err := validEntry(q, e); err != nil {
			v.Issues = append(v.Issues, err.Error())
			continue
		}
		v.Entries = append(v.Entries, verifyEntryContext(ctx, e, deep))
	}
	return v, nil
}

// RecoverBatch reconciles only identities it can prove. It never moves or
// deletes payload data. Dry-run is the default at the CLI boundary.
func RecoverBatch(dir, name string, live bool) (Verification, error) {
	return RecoverBatchContext(context.Background(), dir, name, live)
}
func RecoverBatchContext(ctx context.Context, dir, name string, live bool) (Verification, error) {
	v, err := VerifyBatchContext(ctx, dir, name, true)
	if err != nil {
		return v, err
	}
	m, err := readManifest(dir, name)
	if err != nil {
		return v, err
	}
	if len(v.Issues) > 0 || len(v.Entries) != len(m.Entries) {
		return v, fmt.Errorf("batch contains unrecorded or invalid data; preserved for manual review")
	}
	var remaining []QEntry
	for i, e := range m.Entries {
		switch v.Entries[i].Status {
		case "source-intact": // A durable intent was never moved, or restore completed.
		case "pending-moved":
			if e.Identity == nil {
				remaining = append(remaining, e)
			} else {
				e.Pending = false
				remaining = append(remaining, e)
			}
		default:
			remaining = append(remaining, e)
		}
	}
	if live {
		if err := ctx.Err(); err != nil {
			return v, err
		}
		q := &Quarantine{Dir: dir, Batch: name, man: *m}
		q.man.Entries = remaining
		err = q.writeManifest()
		if err == nil {
			return VerifyBatchContext(ctx, dir, name, false)
		}
	}
	return v, err
}
func validEntry(q *Quarantine, e QEntry) error {
	if !filepath.IsAbs(e.From) || filepath.Clean(e.From) == "/" || e.To != q.dest(e.From) {
		return fmt.Errorf("invalid quarantine entry %q -> %q", e.From, e.To)
	}
	if err := safefs.CheckAncestors(e.To); err != nil {
		return err
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
