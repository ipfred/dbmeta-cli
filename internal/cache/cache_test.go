package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/schema"
	"dbmeta-cli/internal/source"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	clk := &clock{t0}
	s := &Store{Root: filepath.Join(t.TempDir(), "dbmeta"), Now: clk.now}
	return s, clk
}

const ttl = 7 * 24 * time.Hour

func req(mode Mode) Request {
	return Request{Instance: "i", Database: "d", Table: "t", Mode: mode, TTL: ttl}
}

// fetcher counts calls and returns a table whose shape depends on version.
type fetcher struct {
	calls   int
	version string
	err     error
}

func (f *fetcher) fetch(context.Context) (*schema.TableSchema, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &schema.TableSchema{
		Server: "h:3306", ServerVersion: "8.0", Engine: "InnoDB", RowsEstimate: int64(f.calls),
		Columns: []schema.Column{{Ordinal: 1, Name: "id", Type: f.version}},
		Indexes: []schema.Index{},
		DDL:     "CREATE TABLE t (id " + f.version + ") AUTO_INCREMENT=" + string(rune('0'+f.calls)),
	}, nil
}

func seed(t *testing.T, s *Store, f *fetcher) {
	t.Helper()
	if _, err := s.Resolve(context.Background(), req(Refresh), f.fetch); err != nil {
		t.Fatal(err)
	}
	f.calls = 0
}

func TestDecisionTable(t *testing.T) {
	connErr := errors.New("boom: " + source.ErrConn.Error())
	type want struct {
		kind    Kind
		err     error // matched with errors.Is when non-nil
		anyErr  bool
		fetches int
		stale   bool
	}
	tests := []struct {
		name     string
		seeded   bool
		ageAfter time.Duration
		mode     Mode
		fetchErr error
		want     want
	}{
		{"auto/no cache -> fetch", false, 0, Auto, nil, want{kind: Fetched, fetches: 1}},
		{"auto/fresh -> hit", true, time.Hour, Auto, nil, want{kind: Hit}},
		{"auto/just inside ttl -> hit", true, ttl - time.Second, Auto, nil, want{kind: Hit}},
		{"auto/exactly ttl -> fetch", true, ttl, Auto, nil, want{kind: Fetched, fetches: 1}},
		{"auto/expired -> fetch", true, 8 * 24 * time.Hour, Auto, nil, want{kind: Fetched, fetches: 1}},
		{"auto/expired+fail -> stale fallback", true, 8 * 24 * time.Hour, Auto, connErr, want{kind: StaleFallback, fetches: 1, stale: true}},
		{"auto/expired+auth fail -> stale fallback", true, 8 * 24 * time.Hour, Auto, source.ErrAuth, want{kind: StaleFallback, fetches: 1, stale: true}},
		{"auto/expired+table gone -> error", true, 8 * 24 * time.Hour, Auto, source.ErrNotFound, want{err: source.ErrNotFound, fetches: 1}},
		{"auto/no cache+fail -> error", false, 0, Auto, connErr, want{anyErr: true, fetches: 1}},

		{"refresh/no cache -> fetch", false, 0, Refresh, nil, want{kind: Fetched, fetches: 1}},
		{"refresh/fresh -> still fetch", true, time.Hour, Refresh, nil, want{kind: Fetched, fetches: 1}},
		{"refresh/fresh+fail -> error, no fallback", true, time.Hour, Refresh, connErr, want{anyErr: true, fetches: 1}},

		{"cached/no cache -> miss", false, 0, CachedOnly, nil, want{err: ErrMiss}},
		{"cached/fresh -> hit", true, time.Hour, CachedOnly, nil, want{kind: Hit}},
		{"cached/expired -> stale content, no connect", true, 30 * 24 * time.Hour, CachedOnly, nil, want{kind: StaleCached, stale: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, clk := newStore(t)
			f := &fetcher{version: "int"}
			if tc.seeded {
				seed(t, s, f)
			}
			clk.advance(tc.ageAfter)
			f.err = tc.fetchErr

			out, err := s.Resolve(context.Background(), req(tc.mode), f.fetch)
			if f.calls != tc.want.fetches {
				t.Errorf("fetch calls = %d, want %d", f.calls, tc.want.fetches)
			}
			if tc.want.err != nil || tc.want.anyErr {
				if err == nil || (tc.want.err != nil && !errors.Is(err, tc.want.err)) {
					t.Fatalf("err = %v, want %v", err, tc.want.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != tc.want.kind {
				t.Errorf("kind = %v, want %v", out.Kind, tc.want.kind)
			}
			if out.Schema.Stale != tc.want.stale {
				t.Errorf("stale = %v, want %v", out.Schema.Stale, tc.want.stale)
			}
			if out.Kind == StaleFallback && out.FallbackErr == nil {
				t.Error("fallback must carry the reason")
			}
		})
	}
}

func TestFetchedWritesCacheAtomically(t *testing.T) {
	s, _ := newStore(t)
	f := &fetcher{version: "int"}
	out, err := s.Resolve(context.Background(), req(Auto), f.fetch)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(s.Root, "i", "d", "t.md")
	if out.Path != want {
		t.Errorf("path = %s", out.Path)
	}
	ents, _ := os.ReadDir(filepath.Dir(want))
	if len(ents) != 1 || ents[0].Name() != "t.md" {
		t.Errorf("leftover temp files: %v", ents)
	}
	back, err := s.Read("i", "d", "t")
	if err != nil {
		t.Fatal(err)
	}
	if back.SchemaHash == "" || !back.FetchedAt.Equal(t0) || back.Instance != "i" || back.Stale {
		t.Errorf("%+v", back)
	}
}

func TestFallbackPersistsStaleFlag(t *testing.T) {
	s, clk := newStore(t)
	f := &fetcher{version: "int"}
	seed(t, s, f)
	clk.advance(10 * 24 * time.Hour)
	f.err = source.ErrConn
	if _, err := s.Resolve(context.Background(), req(Auto), f.fetch); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Root, "i", "d", "t.md"))
	if !strings.Contains(string(b), "stale: true") {
		t.Errorf("file should be marked stale:\n%s", b)
	}
	if !strings.Contains(string(b), "fetched_at: "+t0.Format(time.RFC3339)) {
		t.Error("fallback must keep the original fetched_at")
	}
	// A later successful fetch clears the flag.
	f.err = nil
	out, err := s.Resolve(context.Background(), req(Auto), f.fetch)
	if err != nil || out.Kind != Fetched || out.Schema.Stale {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestCachedModeDoesNotRewriteExpiredEntry(t *testing.T) {
	s, clk := newStore(t)
	f := &fetcher{version: "int"}
	seed(t, s, f)
	p := filepath.Join(s.Root, "i", "d", "t.md")
	before, _ := os.ReadFile(p)
	clk.advance(30 * 24 * time.Hour)
	if _, err := s.Resolve(context.Background(), req(CachedOnly), f.fetch); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("--cached must not modify the cache")
	}
}

func TestChangeDetection(t *testing.T) {
	s, clk := newStore(t)
	f := &fetcher{version: "int"}
	seed(t, s, f)

	// Same structure (only row estimate and AUTO_INCREMENT move): not a change.
	clk.advance(time.Hour)
	out, err := s.Resolve(context.Background(), req(Refresh), f.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if out.Changed || out.Schema.ChangedAt != nil {
		t.Errorf("unchanged structure reported as changed: %+v", out)
	}

	// A column type changes.
	clk.advance(time.Hour)
	f.version = "bigint"
	out, err = s.Resolve(context.Background(), req(Refresh), f.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Changed || out.Schema.ChangedAt == nil || !out.Schema.ChangedAt.Equal(clk.t) || !out.PreviousAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("change not detected: %+v", out)
	}
	back, _ := s.Read("i", "d", "t")
	if back.ChangedAt == nil {
		t.Error("changed_at must be persisted")
	}

	// A later refresh with no further change keeps the earlier changed_at.
	clk.advance(time.Hour)
	out, _ = s.Resolve(context.Background(), req(Refresh), f.fetch)
	if out.Changed || out.Schema.ChangedAt == nil || !out.Schema.ChangedAt.Equal(back.ChangedAt.Add(0)) {
		t.Errorf("changed_at should carry over: %+v", out.Schema.ChangedAt)
	}
}

func TestCorruptCacheIsTreatedAsMiss(t *testing.T) {
	s, _ := newStore(t)
	p := filepath.Join(s.Root, "i", "d", "t.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("garbage"), 0o644)

	f := &fetcher{version: "int"}
	out, err := s.Resolve(context.Background(), req(Auto), f.fetch)
	if err != nil || out.Kind != Fetched || out.CacheWarn == nil {
		t.Fatalf("auto should refetch and warn: %v %+v", err, out)
	}
	os.WriteFile(p, []byte("garbage"), 0o644)
	if _, err := s.Resolve(context.Background(), req(CachedOnly), f.fetch); !errors.Is(err, ErrMiss) {
		t.Errorf("cached mode on corrupt entry should be a miss, got %v", err)
	}
}

func TestFutureFetchedAtCountsAsFresh(t *testing.T) {
	s, clk := newStore(t)
	f := &fetcher{version: "int"}
	seed(t, s, f)
	clk.advance(-48 * time.Hour)
	out, err := s.Resolve(context.Background(), req(Auto), f.fetch)
	if err != nil || out.Kind != Hit || out.Age != 0 {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestZeroTTLAlwaysFetches(t *testing.T) {
	s, _ := newStore(t)
	f := &fetcher{version: "int"}
	seed(t, s, f)
	r := req(Auto)
	r.TTL = 0
	out, err := s.Resolve(context.Background(), r, f.fetch)
	if err != nil || out.Kind != Fetched {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	s, _ := newStore(t)
	for _, c := range [][3]string{{"..", "d", "t"}, {"i", "..", "t"}, {"i", "d", "../x"}, {"i/j", "d", "t"}, {"i", "a/b", "t"}, {"i", "d", "t.md"}} {
		if _, err := s.Path(c[0], c[1], c[2]); !errors.Is(err, guard.ErrInvalid) {
			t.Errorf("%v: want ErrInvalid, got %v", c, err)
		}
	}
	f := &fetcher{}
	if _, err := s.Resolve(context.Background(), Request{Instance: "i", Database: "..", Table: "t"}, f.fetch); !errors.Is(err, guard.ErrInvalid) || f.calls != 0 {
		t.Errorf("bad identifiers must fail before fetching: %v calls=%d", err, f.calls)
	}
}

func TestListAndClear(t *testing.T) {
	s, clk := newStore(t)
	f := &fetcher{version: "int"}
	for _, r := range []Request{
		{Instance: "a", Database: "d1", Table: "t1"}, {Instance: "a", Database: "d1", Table: "t2"},
		{Instance: "a", Database: "d2", Table: "t3"}, {Instance: "b", Database: "d1", Table: "t1"},
	} {
		r.Mode, r.TTL = Refresh, ttl
		if _, err := s.Resolve(context.Background(), r, f.fetch); err != nil {
			t.Fatal(err)
		}
	}
	clk.advance(48 * time.Hour)

	all, err := s.List("", "")
	if err != nil || len(all) != 4 {
		t.Fatalf("%v %v", all, err)
	}
	if all[0].Instance != "a" || all[0].Table != "t1" || all[0].Age != 48*time.Hour {
		t.Errorf("order/age wrong: %+v", all[0])
	}
	if l, _ := s.List("a", "d1"); len(l) != 2 {
		t.Errorf("a/d1: %v", l)
	}
	if dbs, tabs := s.Count("a"); dbs != 2 || tabs != 3 {
		t.Errorf("count = %d,%d", dbs, tabs)
	}
	if dbs, tabs := s.Count("zzz"); dbs != 0 || tabs != 0 {
		t.Errorf("empty count = %d,%d", dbs, tabs)
	}

	if n, err := s.Clear("a", "d1", "t1"); err != nil || n != 1 {
		t.Errorf("clear table: %d %v", n, err)
	}
	if n, _ := s.Clear("a", "d1", "t1"); n != 0 {
		t.Errorf("clearing a missing table should remove 0, got %d", n)
	}
	if n, err := s.Clear("a", "d1", ""); err != nil || n != 1 {
		t.Errorf("clear db: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "a", "d1")); !os.IsNotExist(err) {
		t.Error("empty database directory should be gone")
	}
	if n, err := s.Clear("a", "", ""); err != nil || n != 1 {
		t.Errorf("clear instance: %d %v", n, err)
	}
	if l, _ := s.List("", ""); len(l) != 1 || l[0].Instance != "b" {
		t.Errorf("remaining: %v", l)
	}
	if _, err := s.Clear("a", "", "t"); err == nil {
		t.Error("table without db must be rejected")
	}
	if _, err := s.Clear("..", "", ""); err == nil {
		t.Error("traversal must be rejected")
	}
	if _, err := os.Stat(s.Root); err != nil {
		t.Error("cache root must survive")
	}
}

func TestListFlagsUnreadable(t *testing.T) {
	s, _ := newStore(t)
	p := filepath.Join(s.Root, "i", "d", "t.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("nope"), 0o644)
	os.WriteFile(filepath.Join(s.Root, "i", "d", "notes.txt"), []byte("x"), 0o644)
	l, err := s.List("i", "")
	if err != nil || len(l) != 1 || !l[0].Unreadable {
		t.Fatalf("%v %+v", err, l)
	}
}
