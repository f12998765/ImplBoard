// Package diskstore keeps the dashboard reports on local disk instead of the
// browser, so a report survives restarts and does not count against the
// browser's 5 MB quota.
//
// Layout: <root>/<scope>/<account>/<app>/<day>.json and
// <root>/<scope>/<account>/<day>.json when app is empty. The account folder is
// the credential id, which the backends derive from host+appId (or wsid+token)
// and therefore keeps stable when only the cURL is refreshed.
//
// Each file is {"ts": <unix ms>, "data": <report>}. A missing file is a cache
// miss, never an error: callers fall back to the upstream API.
package diskstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultMaxBytes caps the whole tree (all scopes together).
const DefaultMaxBytes int64 = 500 * 1024 * 1024

// Entry is one cached report plus the moment it was fetched.
type Entry struct {
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data"`
}

// Store is safe for concurrent use.
type Store struct {
	root     string
	maxBytes int64

	mu   sync.Mutex
	used int64
}

// Open prepares <root> and measures what is already there.
func Open(root string, maxBytes int64) (*Store, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root, maxBytes: maxBytes}
	s.used = s.measure()
	return s, nil
}

// Root is the directory the store owns.
func (s *Store) Root() string { return s.root }

func (s *Store) path(scope, account, app, day string) string {
	parts := []string{s.root, safe(scope), safe(account)}
	if app != "" {
		parts = append(parts, safe(app))
	}
	parts = append(parts, safe(day)+".json")
	return filepath.Join(parts...)
}

// safe keeps a key component from escaping the tree.
func safe(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return "_"
	}
	return out
}

// Get returns the cached report for one key.
func (s *Store) Get(scope, account, app, day string) (*Entry, bool) {
	raw, err := os.ReadFile(s.path(scope, account, app, day))
	if err != nil {
		return nil, false
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil || len(e.Data) == 0 {
		return nil, false
	}
	return &e, true
}

// Put writes (or overwrites) one key and evicts the oldest files when the
// store grew past its cap. A date already cached is simply replaced, other
// dates are untouched.
func (s *Store) Put(scope, account, app, day string, ts int64, data json.RawMessage) error {
	if len(data) == 0 {
		return errors.New("diskstore: empty payload")
	}
	p := s.path(scope, account, app, day)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(Entry{TS: ts, Data: data})
	if err != nil {
		return err
	}
	if err := writeAtomic(p, body); err != nil {
		return err
	}

	s.mu.Lock()
	s.used = s.measure()
	over := s.used - s.maxBytes
	s.mu.Unlock()
	if over > 0 {
		s.evict(over)
	}
	return nil
}

// Days lists the cached dates of one account (+app when app is not empty).
func (s *Store) Days(scope, account, app string) []string {
	dir := filepath.Join(s.root, safe(scope), safe(account))
	if app != "" {
		dir = filepath.Join(dir, safe(app))
	}
	out := []string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		day := strings.TrimSuffix(name, ".json")
		if len(day) == 10 {
			out = append(out, day)
		}
	}
	sort.Strings(out)
	return out
}

// Size reports how many bytes the account folder occupies.
func (s *Store) Size(scope, account string) int64 {
	dir := filepath.Join(s.root, safe(scope), safe(account))
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Total reports the whole tree size.
func (s *Store) Total() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// Drop removes everything cached for one account (used when it is deleted).
func (s *Store) Drop(scope, account string) error {
	dir := filepath.Join(s.root, safe(scope), safe(account))
	if filepath.Clean(dir) == filepath.Clean(s.root) {
		return errors.New("diskstore: refusing to drop the root")
	}
	err := os.RemoveAll(dir)
	s.mu.Lock()
	s.used = s.measure()
	s.mu.Unlock()
	return err
}

// Clear removes every cached report of one scope.
func (s *Store) Clear(scope string) error {
	dir := filepath.Join(s.root, safe(scope))
	if filepath.Clean(dir) == filepath.Clean(s.root) {
		return errors.New("diskstore: refusing to clear the root")
	}
	err := os.RemoveAll(dir)
	s.mu.Lock()
	s.used = s.measure()
	s.mu.Unlock()
	return err
}

func (s *Store) measure() int64 {
	var total int64
	_ = filepath.WalkDir(s.root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// evict deletes the oldest files (by mtime) until at least need bytes are gone.
func (s *Store) evict(need int64) {
	type file struct {
		path string
		mod  int64
		size int64
	}
	files := make([]file, 0, 64)
	_ = filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			files = append(files, file{path: p, mod: info.ModTime().UnixNano(), size: info.Size()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		if need <= 0 {
			break
		}
		if os.Remove(f.path) == nil {
			need -= f.size
		}
	}
	s.mu.Lock()
	s.used = s.measure()
	s.mu.Unlock()
}

// writeAtomic writes through a temp file so a crash cannot leave a half file.
func writeAtomic(p string, body []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
