package routes

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

func TestReplaceBuildsOneToMany(t *testing.T) {
	r := New()
	r.Replace("prod", []string{"bdp_ent", "temp_x"}, t0)
	r.Replace("test", []string{"temp_x"}, t0)
	if got := r.Lookup("temp_x", nil); !reflect.DeepEqual(got, []string{"prod", "test"}) {
		t.Errorf("temp_x -> %v", got)
	}
	if got := r.Lookup("bdp_ent", nil); !reflect.DeepEqual(got, []string{"prod"}) {
		t.Errorf("bdp_ent -> %v", got)
	}
	if got := r.Lookup("nope", nil); len(got) != 0 {
		t.Errorf("nope -> %v", got)
	}
}

func TestReplaceRemovesDroppedDatabases(t *testing.T) {
	r := New()
	r.Replace("prod", []string{"a", "b"}, t0)
	r.Replace("prod", []string{"a"}, t0.Add(time.Hour))
	if _, ok := r.Databases["b"]; ok {
		t.Error("b should be gone once prod no longer has it")
	}
	if !r.SyncedAt("prod").Equal(t0.Add(time.Hour)) {
		t.Errorf("synced_at = %v", r.SyncedAt("prod"))
	}
	if !r.SyncedAt("never").IsZero() {
		t.Error("unknown instance should report the zero time")
	}
}

func TestMarkAttemptKeepsDatabases(t *testing.T) {
	r := New()
	r.Replace("a", []string{"d"}, t0)
	r.MarkAttempt("a", t0.Add(2*time.Hour))
	r.MarkAttempt("b", t0.Add(3*time.Hour))
	if got := r.Lookup("d", nil); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("a failed attempt must not drop databases: %v", got)
	}
	if !r.SyncedAt("a").Equal(t0) || !r.LastTried("a").Equal(t0.Add(2*time.Hour)) {
		t.Errorf("synced=%v tried=%v", r.SyncedAt("a"), r.LastTried("a"))
	}
	if !r.SyncedAt("b").IsZero() || !r.LastTried("b").Equal(t0.Add(3*time.Hour)) {
		t.Errorf("b: synced=%v tried=%v", r.SyncedAt("b"), r.LastTried("b"))
	}
	if !r.LastTried("none").IsZero() {
		t.Error("unknown instance has never been tried")
	}
}

func TestPruneAndLookupFilter(t *testing.T) {
	r := New()
	r.Replace("a", []string{"d"}, t0)
	r.Replace("b", []string{"d"}, t0)
	if got := r.Lookup("d", func(n string) bool { return n == "b" }); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("filtered lookup = %v", got)
	}
	if removed := r.Prune([]string{"b"}); !reflect.DeepEqual(removed, []string{"a"}) {
		t.Errorf("removed = %v", removed)
	}
	if got := r.Lookup("d", nil); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("after prune = %v", got)
	}
	if _, ok := r.Instances["a"]; ok {
		t.Error("pruned instance metadata should be gone")
	}
}

func TestSaveLoadRoundTripIsStable(t *testing.T) {
	dir := t.TempDir()
	r := New()
	r.Replace("z", []string{"b", "a"}, t0)
	r.Replace("m", []string{"a"}, t0)
	if err := Save(dir, r); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(Path(dir))
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, got); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(Path(dir))
	if string(first) != string(second) {
		t.Errorf("not stable:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(string(first), `"a": [
      "m",
      "z"
    ]`) {
		t.Errorf("instances should be sorted:\n%s", first)
	}
}

func TestLoadMissingIsEmptyAndBadFilesFail(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil || len(r.Databases) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	for name, body := range map[string]string{
		"garbage":      `{`,
		"bad db":       `{"version":1,"databases":{"a`+"`"+`b":["x"]}}`,
		"bad instance": `{"version":1,"databases":{"a":["../x"]}}`,
		"future":       `{"version":99}`,
	} {
		if err := os.WriteFile(Path(dir), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
