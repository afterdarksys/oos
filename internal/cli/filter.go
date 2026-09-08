package cli

import (
	"fmt"
	"strings"

	"github.com/afterdarksys/oos/internal/size"
)

func filterFromOpts(o *opts) (size.Filter, error) {
	f := size.Filter{SortBy: strings.ToLower(strings.TrimSpace(o.sortBy)), Top: o.top, Tag: strings.TrimSpace(o.tag)}
	var err error
	if f.OlderThan, err = size.ParseAge(o.olderThan); err != nil {
		return f, err
	}
	if f.NewerThan, err = size.ParseAge(o.newerThan); err != nil {
		return f, err
	}
	switch f.SortBy {
	case "", "size":
		f.SortBy = "size"
	case "oldest", "newest", "name":
	default:
		return f, fmt.Errorf("--sort %q: want size, oldest, newest or name", o.sortBy)
	}
	if strings.TrimSpace(o.ext) != "" {
		f.Exts = map[string]bool{}
		for _, e := range strings.Split(o.ext, ",") {
			e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if e != "" {
				f.Exts[e] = true
			}
		}
	}
	if f.Top < 0 {
		return f, fmt.Errorf("--top must be >= 0")
	}
	return f, nil
}
