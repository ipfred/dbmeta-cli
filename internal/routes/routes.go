// Package routes keeps the database -> instance mapping in .dbmeta/routes.json.
// The file holds no credentials, so unlike config.json it may be committed.
package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"dbmeta-cli/internal/guard"
)

const (
	FileName = "routes.json"
	version  = 1
)

type InstanceMeta struct {
	SyncedAt    *time.Time `json:"synced_at,omitempty"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"` // last try, successful or not; throttles auto-sync
}

// Routes maps every database name to the instances that have it. One database
// on several instances (test/prod, primary/replica) is the normal one-to-many case.
type Routes struct {
	Version   int                      `json:"version"`
	Instances map[string]*InstanceMeta `json:"instances"`
	Databases map[string][]string      `json:"databases"`
}

func New() *Routes {
	return &Routes{Version: version, Instances: map[string]*InstanceMeta{}, Databases: map[string][]string{}}
}

func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads routes.json. A missing file is an empty mapping, not an error.
func Load(dir string) (*Routes, error) {
	b, err := os.ReadFile(Path(dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return nil, err
	}
	r := New()
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w(可删除后重新运行 dbmeta-cli db sync)", Path(dir), err)
	}
	if r.Version > version {
		return nil, fmt.Errorf("%s 的版本 %d 高于当前工具支持的版本 %d,请升级 dbmeta-cli", Path(dir), r.Version, version)
	}
	if r.Instances == nil {
		r.Instances = map[string]*InstanceMeta{}
	}
	if r.Databases == nil {
		r.Databases = map[string][]string{}
	}
	for db, insts := range r.Databases {
		if err := guard.Ident("库名", db); err != nil {
			return nil, fmt.Errorf("%s: %w", Path(dir), err)
		}
		for _, n := range insts {
			if err := guard.InstanceName(n); err != nil {
				return nil, fmt.Errorf("%s: %w", Path(dir), err)
			}
		}
	}
	return r, nil
}

// Save writes the file atomically. encoding/json sorts map keys; instance
// lists are kept sorted by Replace, so the output is stable for git.
func Save(dir string, r *Routes) error {
	r.Version = version
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".routes-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path(dir))
}

// Replace makes dbs the complete database list of an instance.
func (r *Routes) Replace(instance string, dbs []string, at time.Time) {
	r.remove(instance)
	for _, db := range dbs {
		r.Databases[db] = insertSorted(r.Databases[db], instance)
	}
	t := at.Truncate(time.Second)
	r.Instances[instance] = &InstanceMeta{SyncedAt: &t, AttemptedAt: &t}
}

// MarkAttempt records a failed sync without touching the instance's databases.
func (r *Routes) MarkAttempt(instance string, at time.Time) {
	m := r.Instances[instance]
	if m == nil {
		m = &InstanceMeta{}
		r.Instances[instance] = m
	}
	t := at.Truncate(time.Second)
	m.AttemptedAt = &t
}

// LastTried is the latest sync attempt (zero if there was none).
func (r *Routes) LastTried(instance string) time.Time {
	m := r.Instances[instance]
	if m == nil {
		return time.Time{}
	}
	var last time.Time
	for _, p := range []*time.Time{m.SyncedAt, m.AttemptedAt} {
		if p != nil && p.After(last) {
			last = *p
		}
	}
	return last
}

// Prune drops every instance that is not in keep.
func (r *Routes) Prune(keep []string) (removed []string) {
	ok := map[string]bool{}
	for _, n := range keep {
		ok[n] = true
	}
	known := map[string]bool{}
	for n := range r.Instances {
		known[n] = true
	}
	for _, insts := range r.Databases {
		for _, n := range insts {
			known[n] = true
		}
	}
	for n := range known {
		if !ok[n] {
			r.remove(n)
			removed = append(removed, n)
		}
	}
	sort.Strings(removed)
	return removed
}

func (r *Routes) remove(instance string) {
	delete(r.Instances, instance)
	for db, insts := range r.Databases {
		out := insts[:0:0]
		for _, n := range insts {
			if n != instance {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			delete(r.Databases, db)
		} else {
			r.Databases[db] = out
		}
	}
}

// Lookup returns the instances that have db, sorted. Instances that are no
// longer configured are filtered out using valid.
func (r *Routes) Lookup(db string, valid func(string) bool) []string {
	var out []string
	for _, n := range r.Databases[db] {
		if valid == nil || valid(n) {
			out = append(out, n)
		}
	}
	return out
}

// SyncedAt is the zero time for an instance that has never been synced.
func (r *Routes) SyncedAt(instance string) time.Time {
	if m := r.Instances[instance]; m != nil && m.SyncedAt != nil {
		return *m.SyncedAt
	}
	return time.Time{}
}

func insertSorted(list []string, s string) []string {
	i := sort.SearchStrings(list, s)
	if i < len(list) && list[i] == s {
		return list
	}
	list = append(list, "")
	copy(list[i+1:], list[i:])
	list[i] = s
	return list
}
