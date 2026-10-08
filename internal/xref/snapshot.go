package xref

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// MapSnapshot is a Snapshot held in memory, path to content.
type MapSnapshot map[string][]byte

// Files implements Snapshot.
func (m MapSnapshot) Files() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Read implements Snapshot.
func (m MapSnapshot) Read(p string) ([]byte, bool) {
	b, ok := m[p]
	return b, ok
}

// LoadDir reads the source files under root into a MapSnapshot, skipping the
// trees skipPath names and anything xref would not search anyway. maxBytes
// bounds the total read; files past it are left out.
func LoadDir(root string, maxBytes int64) (MapSnapshot, error) {
	out := MapSnapshot{}
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry is left out, not fatal
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// skipPath already names .git; other dot-directories such as
			// .github/scripts hold source like any other tree.
			if rel != "." && skipPath(rel+"/x") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || langOf(rel) == nil || skipPath(rel) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > int64(DefaultLimits.MaxFileBytes) || total+info.Size() > maxBytes {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		total += int64(len(b))
		out[rel] = b
		return nil
	})
	return out, err
}

// Searchable reports whether xref would search p: source in a language it
// knows, outside vendored and generated trees. Fetchers use it to skip
// everything else before reading it.
func Searchable(p string) bool { return langOf(p) != nil && !skipPath(p) }
