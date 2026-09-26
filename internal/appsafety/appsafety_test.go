package appsafety

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/afterdarksys/oos/internal/testutil"
)

func TestClassifyCacheVersusBookmarks(t *testing.T) {
	home := t.TempDir()
	lib := filepath.Join(home, "Library")
	cases := []struct {
		rel   string
		class Class
		why   string
	}{
		{"Caches/com.apple.Safari/fsCachedData", Disposable, "app cache; regenerates"},
		{"Caches/com.google.Chrome", Disposable, "app cache; regenerates"},
		{"Logs/Chrome", Disposable, "logs; regenerates"},
		{"Saved Application State/com.old.app.savedState", Disposable, "window restore state; regenerates"},
		{"Application Support/Google/Chrome/Default/Cache/index", Disposable, "app cache; regenerates"},
		{"Application Support/Google/Chrome/Default/Code Cache/js", Disposable, "browser code cache; regenerates"},
		{"Application Support/Google/Chrome/Default/Service Worker/CacheStorage/x", Disposable, "service worker cache; regenerates"},
		{"Application Support/Firefox/Profiles/abc.default/cache2/entries", Disposable, "Firefox disk cache; regenerates"},
		{"Containers/com.apple.Safari/Data/Library/Caches/WebKit", Disposable, "app cache; regenerates"},
		{"Application Support/Google/Chrome/Default/Bookmarks", Keep, "bookmarks"},
		{"Application Support/Google/Chrome/Default/Cookies", Keep, "cookies"},
		{"Application Support/Google/Chrome/Default/Login Data", Keep, "saved passwords"},
		{"Application Support/Firefox/Profiles/abc.default/places.sqlite", Keep, "bookmarks and history"},
		{"Application Support/Firefox/Profiles/abc.default/logins.json", Keep, "saved passwords"},
		{"Safari/Bookmarks.plist", Keep, "bookmarks"},
		{"Application Support/Google/Chrome", Keep, "application data; bookmarks, cookies, passwords and mail live here"},
		{"Containers/com.apple.Safari", Keep, "app container; user data, not safe to remove as a whole"},
		{"Preferences/com.apple.Safari.plist", Keep, "settings"},
		{"Mail/V10/MailData", Keep, "mail"},
		{"Messages/chat.db", Keep, "messages"},
		{"Cache/Bookmarks", Keep, "bookmarks"},
	}
	for _, c := range cases {
		got, why := Classify(filepath.Join(lib, c.rel), home)
		if got != c.class || why != c.why {
			t.Errorf("%s: %q %q want %q %q", c.rel, got, why, c.class, c.why)
		}
	}
	if class, _ := Classify(filepath.Join(home, "development", "Library", "Caches"), home); class != "" {
		t.Errorf("a Library outside ~/Library is unknown, got %q", class)
	}
	if class, why := Area("Application Support"); class != Keep || why == "" {
		t.Errorf("area: %q %q", class, why)
	}
	if class, _ := Area("Caches"); class != Disposable {
		t.Errorf("caches area: %q", class)
	}
}

func TestFindSeparatesCacheFromBookmarks(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	testutil.Write(t, filepath.Join(root, "Default", "Cache", "data_0"), 2<<20)
	testutil.Write(t, filepath.Join(root, "Default", "Bookmarks"), 4096)
	testutil.Write(t, filepath.Join(root, "Default", "Login Data"), 8192)
	testutil.Write(t, filepath.Join(root, "Default", "Service Worker", "CacheStorage", "blob"), 2<<20)
	// a symlink named Cache must not be followed
	if err := os.Symlink(filepath.Join(root, "Default", "Bookmarks"), filepath.Join(root, "Default", "CacheLink")); err != nil {
		t.Fatal(err)
	}
	safe, kept := Find(root, 1<<20)
	if len(safe) != 2 {
		t.Fatalf("safe dirs: %+v", safe)
	}
	for _, h := range safe {
		if h.Class != Disposable {
			t.Errorf("safe hit not disposable: %+v", h)
		}
		if filepath.Base(h.Path) == "Bookmarks" {
			t.Errorf("bookmarks offered as disposable: %+v", h)
		}
	}
	foundBookmark := false
	for _, h := range kept {
		if h.Class != Keep {
			t.Errorf("kept hit: %+v", h)
		}
		if filepath.Base(h.Path) == "Bookmarks" {
			foundBookmark = true
		}
	}
	if !foundBookmark {
		t.Errorf("bookmarks file not reported: %+v", kept)
	}
}
