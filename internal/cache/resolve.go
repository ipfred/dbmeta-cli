package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dbmeta-cli/internal/schema"
	"dbmeta-cli/internal/source"
)

type Mode int

const (
	// Auto serves a fresh cache entry and queries the database otherwise.
	Auto Mode = iota
	// Refresh always queries the database and never falls back to old data.
	Refresh
	// CachedOnly never connects.
	CachedOnly
)

// Kind says where the returned schema came from.
type Kind int

const (
	Hit           Kind = iota // fresh cache entry
	Fetched                   // queried the database and updated the cache
	StaleCached               // CachedOnly returned an entry past its TTL
	StaleFallback             // the query failed, so an old entry was returned
)

type Request struct {
	Instance, Database, Table string
	Mode                      Mode
	TTL                       time.Duration
}

// FetchFunc queries the database. It must not touch the cache.
type FetchFunc func(ctx context.Context) (*schema.TableSchema, error)

type Outcome struct {
	Schema *schema.TableSchema
	Kind   Kind
	Age    time.Duration // age of the cached entry that was considered, if any
	// Changed is set on Fetched when the structure differs from the previous cache entry.
	Changed     bool
	PreviousAt  time.Time // fetched_at of the previous entry when Changed
	FallbackErr error     // why the query failed, for StaleFallback
	CacheWarn   error     // an unreadable cache entry that was ignored
	Path        string
}

// Resolve implements the decision table:
//
//	Refresh     -> query; failure is an error
//	CachedOnly  -> entry or ErrMiss; past TTL is returned as StaleCached
//	Auto        -> fresh entry; else query; query failure falls back to an old
//	               entry (marked stale) except when the table is gone (ErrNotFound)
func (s *Store) Resolve(ctx context.Context, req Request, fetch FetchFunc) (*Outcome, error) {
	path, err := s.Path(req.Instance, req.Database, req.Table)
	if err != nil {
		return nil, err
	}
	out := &Outcome{Path: path}

	// Refresh still reads the old entry, for change detection.
	old, err := s.Read(req.Instance, req.Database, req.Table)
	if err != nil {
		if err != ErrMiss { // present but unreadable: ignore it, but say so
			out.CacheWarn = err
		}
		old = nil
	}
	now := s.now()
	if old != nil {
		out.Age = Age(old, now)
	}

	switch req.Mode {
	case CachedOnly:
		if old == nil {
			if out.CacheWarn != nil {
				return nil, out.CacheWarn
			}
			return nil, fmt.Errorf("%w: %s/%s/%s", ErrMiss, req.Instance, req.Database, req.Table)
		}
		out.Schema = old
		out.Kind = Hit
		if !Fresh(old, req.TTL, now) {
			out.Kind = StaleCached
			old.Stale = true
		}
		return out, nil

	case Auto:
		if old != nil && Fresh(old, req.TTL, now) {
			out.Schema, out.Kind = old, Hit
			return out, nil
		}
	}

	ts, err := fetch(ctx)
	if err != nil {
		if req.Mode == Auto && old != nil && !errors.Is(err, source.ErrNotFound) {
			old.Stale = true
			// Persist the flag so someone browsing the files sees it too. A
			// write failure must not hide the data we already have.
			_ = s.Write(old)
			out.Schema, out.Kind, out.FallbackErr = old, StaleFallback, err
			return out, nil
		}
		return nil, err
	}

	ts.Instance, ts.Database, ts.Table = req.Instance, req.Database, req.Table
	ts.FetchedAt = now.Truncate(time.Second)
	ts.SchemaHash = ts.ComputeHash()
	ts.Stale = false
	ts.ChangedAt = nil
	if old != nil {
		ts.ChangedAt = old.ChangedAt
		if old.SchemaHash != ts.SchemaHash {
			t := ts.FetchedAt
			ts.ChangedAt = &t
			out.Changed, out.PreviousAt = true, old.FetchedAt
		}
	}
	if err := s.Write(ts); err != nil {
		return nil, fmt.Errorf("写入缓存失败: %w", err)
	}
	out.Schema, out.Kind = ts, Fetched
	return out, nil
}
