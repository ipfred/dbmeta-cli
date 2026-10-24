// Package cache stores one Markdown file per table under dbmeta/<instance>/<db>/
// and decides when a stored copy may be served instead of querying the database.
package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/schema"
)

// ErrMiss means there is no cache entry (or none that can be read).
var ErrMiss = errors.New("缓存未命中")

type Store struct {
	Root string           // the dbmeta directory
	Now  func() time.Time // injected so tests control the clock
}

func New(root string) *Store { return &Store{Root: root, Now: time.Now} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Path returns the cache file for a table after validating every component.
func (s *Store) Path(instance, db, table string) (string, error) {
	if err := guard.InstanceName(instance); err != nil {
		return "", err
	}
	if err := guard.Ident("库名", db); err != nil {
		return "", err
	}
	if err := guard.Ident("表名", table); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, instance, db, table+".md"), nil
}

// Read loads an entry. A missing file is ErrMiss; an unparsable one wraps both
// ErrMiss and schema.ErrParse so callers can treat it as a miss but still say why.
func (s *Store) Read(instance, db, table string) (*schema.TableSchema, error) {
	p, err := s.Path(instance, db, table)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrMiss
		}
		return nil, err
	}
	ts, err := schema.ParseMarkdown(string(b))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMiss, p, err)
	}
	return ts, nil
}

// Write stores an entry atomically (temp file in the same directory, then rename).
func (s *Store) Write(ts *schema.TableSchema) error {
	p, err := s.Path(ts.Instance, ts.Database, ts.Table)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(ts.Markdown()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Fresh reports whether an entry is still inside its TTL. A fetched_at in the
// future (clock skew) counts as age zero.
func Fresh(ts *schema.TableSchema, ttl time.Duration, now time.Time) bool {
	return Age(ts, now) < ttl
}

func Age(ts *schema.TableSchema, now time.Time) time.Duration {
	age := now.Sub(ts.FetchedAt)
	if age < 0 {
		return 0
	}
	return age
}

// Entry is one row of `cache list`.
type Entry struct {
	Instance, Database, Table string
	Path                      string
	FetchedAt                 time.Time
	Age                       time.Duration
	Stale                     bool // flagged stale in the file
	Unreadable                bool
}

// List walks the cache. Empty db means every database of the instance; empty
// instance means every instance directory that has a valid name.
func (s *Store) List(instance, db string) ([]Entry, error) {
	var instances []string
	if instance != "" {
		if err := guard.InstanceName(instance); err != nil {
			return nil, err
		}
		instances = []string{instance}
	} else {
		names, err := subdirs(s.Root)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if guard.InstanceName(n) == nil {
				instances = append(instances, n)
			}
		}
	}
	if db != "" {
		if err := guard.Ident("库名", db); err != nil {
			return nil, err
		}
	}

	var out []Entry
	for _, inst := range instances {
		dbs := []string{db}
		if db == "" {
			var err error
			if dbs, err = subdirs(filepath.Join(s.Root, inst)); err != nil {
				return nil, err
			}
		}
		for _, d := range dbs {
			files, err := os.ReadDir(filepath.Join(s.Root, inst, d))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, err
			}
			for _, f := range files {
				name, ok := strings.CutSuffix(f.Name(), ".md")
				if !ok || f.IsDir() || guard.Ident("表名", name) != nil {
					continue
				}
				e := Entry{Instance: inst, Database: d, Table: name, Path: filepath.Join(s.Root, inst, d, f.Name())}
				if ts, err := s.Read(inst, d, name); err != nil {
					e.Unreadable = true
				} else {
					e.FetchedAt, e.Age, e.Stale = ts.FetchedAt, Age(ts, s.now()), ts.Stale
				}
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Instance != b.Instance {
			return a.Instance < b.Instance
		}
		if a.Database != b.Database {
			return a.Database < b.Database
		}
		return a.Table < b.Table
	})
	return out, nil
}

// Clear deletes cache files and returns how many tables were removed. Empty db
// clears the whole instance; empty table clears the whole database.
func (s *Store) Clear(instance, db, table string) (int, error) {
	if err := guard.InstanceName(instance); err != nil {
		return 0, err
	}
	switch {
	case db == "" && table != "":
		return 0, errors.New("指定表名时必须同时指定库名")
	case db == "":
		return s.removeDir(filepath.Join(s.Root, instance))
	case table == "":
		if err := guard.Ident("库名", db); err != nil {
			return 0, err
		}
		n, err := s.removeDir(filepath.Join(s.Root, instance, db))
		s.pruneEmpty(filepath.Join(s.Root, instance))
		return n, err
	}
	p, err := s.Path(instance, db, table)
	if err != nil {
		return 0, err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	s.pruneEmpty(filepath.Dir(p))
	s.pruneEmpty(filepath.Join(s.Root, instance))
	return 1, nil
}

// removeDir deletes a cache subtree. It only ever removes paths below Root
// whose components were validated by the caller.
func (s *Store) removeDir(dir string) (int, error) {
	if rel, err := filepath.Rel(s.Root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return 0, fmt.Errorf("拒绝删除缓存目录之外的路径 %s", dir)
	}
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			n++
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	return n, nil
}

// pruneEmpty removes dir if it is empty; errors are irrelevant (it just stays).
func (s *Store) pruneEmpty(dir string) {
	if dir == s.Root {
		return
	}
	_ = os.Remove(dir)
}

func subdirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Count returns how many databases and tables are cached for an instance.
func (s *Store) Count(instance string) (dbs, tables int) {
	entries, err := s.List(instance, "")
	if err != nil {
		return 0, 0
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Database] = true
	}
	return len(seen), len(entries)
}
