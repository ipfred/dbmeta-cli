package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/routes"
	"dbmeta-cli/internal/source"
)

const (
	hostProd = "10.0.0.1"
	hostTest = "10.0.0.2"
)

// twoInstances registers prod (bdp_ent, temp_x) and test (temp_x), both with passwords.
func twoInstances(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.mustRun("instance", "add", "prod", "--host", hostProd, "--user", "ro", "--env", "prod")
	h.mustRun("instance", "add", "test", "--host", hostTest, "--user", "ro", "--env", "test")
	h.setPassword("prod", "pw")
	h.setPassword("test", "pw")
	h.dbs[hostProd] = []string{"bdp_ent", "temp_x"}
	h.dbs[hostTest] = []string{"temp_x"}
	return h
}

func (h *harness) loadRoutes() *routes.Routes {
	h.t.Helper()
	rt, err := routes.Load(h.dbmetaDir())
	if err != nil {
		h.t.Fatal(err)
	}
	return rt
}

func (h *harness) fetchedFrom(host string) int {
	n := 0
	for _, f := range h.fetches {
		if strings.HasPrefix(f, host+"/") {
			n++
		}
	}
	return n
}

func TestDBSyncAndList(t *testing.T) {
	h := twoInstances(t)
	out := h.mustRun("db", "sync")
	if !strings.Contains(out, "prod  2 个库") || !strings.Contains(out, "test  1 个库") {
		t.Errorf("sync output:\n%s", out)
	}
	rt := h.loadRoutes()
	if got := strings.Join(rt.Lookup("temp_x", nil), ","); got != "prod,test" {
		t.Errorf("temp_x -> %s", got)
	}
	if got := strings.Join(rt.Lookup("bdp_ent", nil), ","); got != "prod" {
		t.Errorf("bdp_ent -> %s", got)
	}
	if h.opens != 2 {
		t.Errorf("opens=%d", h.opens)
	}

	h.opens = 0
	list := h.mustRun("db", "list")
	if h.opens != 0 {
		t.Error("db list must not connect")
	}
	if !strings.Contains(list, "bdp_ent") || !strings.Contains(list, "prod(prod), test(test)") {
		t.Errorf("list:\n%s", list)
	}
	var rows []dbRow
	if err := json.Unmarshal([]byte(h.mustRun("db", "list", "--db", "temp_x", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Database != "temp_x" || len(rows[0].Instances) != 2 || rows[0].Instances[1].Env != "test" {
		t.Errorf("%+v", rows)
	}
	if out := h.mustRun("db", "list", "--db", "nope"); !strings.Contains(out, "没有实例包含库 nope") {
		t.Errorf("%s", out)
	}
}

func TestDBSyncOneInstanceLeavesOthersAlone(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	h.dbs[hostProd] = []string{"bdp_ent", "bdp_new"}
	h.mustRun("db", "sync", "prod")
	rt := h.loadRoutes()
	if len(rt.Lookup("bdp_new", nil)) != 1 || len(rt.Lookup("temp_x", nil)) != 1 || rt.Lookup("temp_x", nil)[0] != "test" {
		t.Errorf("%+v", rt.Databases)
	}
}

func TestDBSyncPartialFailureKeepsOldRecords(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")

	h.listErrs[hostTest] = fmt.Errorf("%w: refused", source.ErrConn)
	h.dbs[hostProd] = []string{"bdp_ent"}
	h.mustRun("db", "sync")
	if !strings.Contains(h.stderr.String(), "实例 test 同步失败") {
		t.Errorf("stderr: %s", h.stderr.String())
	}
	rt := h.loadRoutes()
	if got := strings.Join(rt.Lookup("temp_x", nil), ","); got != "test" {
		t.Errorf("prod dropped temp_x, test keeps its old record: %q", got)
	}

	h.listErrs[hostProd] = fmt.Errorf("%w: denied", source.ErrAuth)
	if code := h.run("db", "sync"); code != ExitAuth {
		t.Errorf("all failed: exit %d, want %d", code, ExitAuth)
	}
	if got := strings.Join(h.loadRoutes().Lookup("bdp_ent", nil), ","); got != "prod" {
		t.Errorf("a total failure must not touch routes.json: %q", got)
	}
}

func TestDBSyncErrorsAndAllowlistAndPrune(t *testing.T) {
	h := newHarness(t)
	if code := h.run("db", "sync"); code != ExitUsage {
		t.Errorf("no project: exit %d", code)
	}
	h.mustRun("init")
	if code := h.run("db", "sync"); code != ExitUsage {
		t.Errorf("no instances: exit %d", code)
	}
	h.mustRun("instance", "add", "a", "--host", hostProd, "--user", "ro", "--allow-db", "keep")
	h.mustRun("instance", "add", "b", "--host", hostTest, "--user", "ro")
	if code := h.run("db", "sync", "nope"); code != ExitUsage {
		t.Errorf("unknown instance: exit %d", code)
	}
	if code := h.run("db", "sync"); code != ExitAuth || h.opens != 0 {
		t.Errorf("empty passwords: exit %d opens %d", code, h.opens)
	}
	h.setPassword("a", "pw")
	h.setPassword("b", "pw")
	h.dbs[hostProd] = []string{"keep", "secret"}
	h.dbs[hostTest] = []string{"other"}
	h.mustRun("db", "sync")
	rt := h.loadRoutes()
	if len(rt.Lookup("secret", nil)) != 0 || len(rt.Lookup("keep", nil)) != 1 {
		t.Errorf("allow_databases must filter routes: %+v", rt.Databases)
	}

	c, _ := config.Load(h.dbmetaDir())
	delete(c.Instances, "b")
	config.Save(h.dbmetaDir(), c)
	h.mustRun("db", "sync")
	if !strings.Contains(h.stderr.String(), "已清理") || len(h.loadRoutes().Lookup("other", nil)) != 0 {
		t.Errorf("removed instances should be pruned: %s", h.stderr.String())
	}
}

func TestTableGetQueriesEveryInstanceThatHasTheDB(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	h.opens = 0

	// Single-instance db: one connection, plain document without a banner.
	out := h.mustRun("table", "get", "bdp_ent.t1")
	if h.opens != 1 || h.fetchedFrom(hostTest) != 0 || !strings.HasPrefix(out, "---\ninstance: prod") {
		t.Errorf("opens=%d fetches=%v\n%s", h.opens, h.fetches, out)
	}

	// One-to-many with identical structure: full text once, a pointer for the other.
	out = h.mustRun("table", "get", "temp_x.t1")
	if !strings.Contains(out, "<!-- temp_x.t1 @ prod -->") || !strings.Contains(out, "<!-- temp_x.t1 @ test: 结构与 prod 相同") {
		t.Errorf("\n%s", out)
	}
	if strings.Count(out, "# prod / temp_x / t1") != 1 || strings.Contains(out, "# test / temp_x / t1") {
		t.Errorf("duplicate structure should not be printed twice:\n%s", out)
	}

	// Different structure: both are printed in full.
	h.extraCols[hostTest] = "other_col"
	out = h.mustRun("table", "get", "temp_x.t2")
	if !strings.Contains(out, "# prod / temp_x / t2") || !strings.Contains(out, "# test / temp_x / t2") || !strings.Contains(out, "| other_col |") {
		t.Errorf("\n%s", out)
	}
}

func TestTableGetPartialHitsAcrossInstances(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")

	h.missing[hostTest+"/temp_x.only_prod"] = true
	out := h.mustRun("table", "get", "temp_x.only_prod")
	if !strings.HasPrefix(out, "---\ninstance: prod") || !strings.Contains(h.stderr.String(), "temp_x.only_prod@test: 已跳过") {
		t.Errorf("stderr: %s\nstdout:\n%s", h.stderr.String(), out)
	}

	// The absence is remembered: asking again does not reconnect to test.
	before := h.fetchedFrom(hostTest)
	h.mustRun("table", "get", "temp_x.only_prod")
	if h.fetchedFrom(hostTest) != before {
		t.Errorf("negative cache missed: %v", h.fetches)
	}
	// --refresh asks again.
	h.mustRun("table", "get", "temp_x.only_prod", "--refresh")
	if h.fetchedFrom(hostTest) != before+1 {
		t.Errorf("--refresh should bypass the negative cache: %v", h.fetches)
	}

	// Missing everywhere: exit 4 and nothing on stdout.
	h.missing[hostProd+"/temp_x.nowhere"] = true
	h.missing[hostTest+"/temp_x.nowhere"] = true
	if code := h.run("table", "get", "temp_x.nowhere"); code != ExitNotFound || h.stdout.Len() != 0 {
		t.Errorf("exit %d stdout %q", code, h.stdout.String())
	}
}

func TestTableGetOneInstanceDownStillServesTheOther(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	h.openErrs[hostTest] = fmt.Errorf("%w: refused", source.ErrConn)
	out := h.mustRun("table", "get", "temp_x.t1")
	if !strings.HasPrefix(out, "---\ninstance: prod") || !strings.Contains(h.stderr.String(), "@test: 已跳过") {
		t.Errorf("stderr: %s\n%s", h.stderr.String(), out)
	}
	h.openErrs[hostProd] = fmt.Errorf("%w: refused", source.ErrConn)
	if code := h.run("table", "get", "temp_x.t9"); code != ExitNetwork {
		t.Errorf("both down: exit %d", code)
	}
}

func TestTableGetBatchReusesOneConnectionPerInstance(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	h.opens = 0
	out := h.mustRun("table", "get", "bdp_ent.a", "bdp_ent.b", "bdp_ent.a")
	if h.opens != 1 || len(h.fetches) != 2 {
		t.Errorf("opens=%d fetches=%v (duplicates must be dropped)", h.opens, h.fetches)
	}
	if !strings.Contains(out, "<!-- bdp_ent.a @ prod -->") || !strings.Contains(out, "<!-- bdp_ent.b @ prod -->") {
		t.Errorf("\n%s", out)
	}

	// Cached tables need no connection and no password.
	h.opens = 0
	h.mustRun("table", "get", "bdp_ent.a", "bdp_ent.b")
	if h.opens != 0 {
		t.Errorf("cache hits connected: opens=%d", h.opens)
	}

	paths := strings.Fields(h.mustRun("table", "get", "bdp_ent.a", "temp_x.t1", "--print-path"))
	if len(paths) != 3 {
		t.Errorf("one path per result (a@prod, t1@prod, t1@test): %v", paths)
	}

	var arr []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("table", "get", "temp_x.t1", "--format", "json")), &arr); err != nil || len(arr) != 2 {
		t.Errorf("multiple results should be a JSON array: %v %v", err, arr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("table", "get", "bdp_ent.a", "--format", "json")), &obj); err != nil || obj["table"] != "a" {
		t.Errorf("a single result should be a JSON object: %v %v", err, obj)
	}
}

func TestTableGetPartialBatchFailure(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	code := h.run("table", "get", "bdp_ent.a", "ghost.t")
	if code != ExitUsage {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(h.stdout.String(), "instance: prod") || !strings.Contains(h.stderr.String(), "ghost") {
		t.Errorf("found tables still print\nstdout: %s\nstderr: %s", h.stdout.String(), h.stderr.String())
	}
}

func TestTableGetSyncsOnAMissAndThrottles(t *testing.T) {
	h := twoInstances(t)

	// No db sync yet: the first lookup fills routes.json by itself.
	h.mustRun("table", "get", "bdp_ent.t1")
	if len(h.listed) != 2 || !strings.Contains(h.stderr.String(), "先同步实例的库名") {
		t.Errorf("listed=%v stderr=%s", h.listed, h.stderr.String())
	}
	if _, err := os.Stat(routes.Path(h.dbmetaDir())); err != nil {
		t.Fatal(err)
	}

	// An unknown database fails with 2 ...
	h.listed = nil
	h.now = h.now.Add(2 * time.Hour)
	if code := h.run("table", "get", "ghost.t"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if len(h.listed) != 2 {
		t.Errorf("stale routes should be refreshed on a miss: %v", h.listed)
	}
	// ... and a repeat within the hour does not hammer the servers.
	h.listed = nil
	if code := h.run("table", "get", "ghost.t"); code != ExitUsage || len(h.listed) != 0 {
		t.Errorf("exit %d listed %v", code, h.listed)
	}
	// A new database shows up after the throttle window.
	h.dbs[hostProd] = append(h.dbs[hostProd], "ghost")
	h.now = h.now.Add(2 * time.Hour)
	h.mustRun("table", "get", "ghost.t")
	if !strings.Contains(h.stdout.String(), "# prod / ghost / t") {
		t.Errorf("stdout: %s", h.stdout.String())
	}
}

func TestTableGetFailedAutoSyncIsThrottled(t *testing.T) {
	h := twoInstances(t)
	h.listErrs[hostProd] = fmt.Errorf("%w: refused", source.ErrConn)
	h.listErrs[hostTest] = fmt.Errorf("%w: refused", source.ErrConn)
	if code := h.run("table", "get", "bdp_ent.t"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	h.listed = nil
	if code := h.run("table", "get", "bdp_ent.t"); code != ExitUsage || len(h.listed) != 0 {
		t.Errorf("a failed sync must be remembered: exit %d listed %v", code, h.listed)
	}
}

func TestTableGetCachedModeNeverSyncs(t *testing.T) {
	h := twoInstances(t)
	if code := h.run("table", "get", "bdp_ent.t", "--cached"); code != ExitUsage || h.opens != 0 {
		t.Errorf("exit %d opens %d", code, h.opens)
	}
	h.mustRun("db", "sync")
	h.mustRun("table", "get", "bdp_ent.t")
	h.opens = 0
	h.mustRun("table", "get", "bdp_ent.t", "--cached")
	if h.opens != 0 {
		t.Errorf("opens=%d", h.opens)
	}
	// temp_x.t is cached nowhere: both instances miss.
	if code := h.run("table", "get", "temp_x.t", "--cached"); code != ExitCacheMiss {
		t.Errorf("exit %d", code)
	}
}

func TestTableGetPinnedInstanceIgnoresRoutes(t *testing.T) {
	h := twoInstances(t)
	out := h.mustRun("table", "get", "-i", "test", "temp_x.t1")
	if len(h.listed) != 0 || h.fetchedFrom(hostProd) != 0 || !strings.HasPrefix(out, "---\ninstance: test") {
		t.Errorf("listed=%v fetches=%v", h.listed, h.fetches)
	}
	if code := h.run("table", "get", "--instance", "nope", "temp_x.t1"); code != ExitUsage {
		t.Errorf("unknown instance: exit %d", code)
	}
}

func TestTableGetRejectsOldArgumentForm(t *testing.T) {
	h := twoInstances(t)
	code := h.run("table", "get", "prod", "bdp_ent", "t1")
	if code != ExitUsage || !strings.Contains(h.stderr.String(), "bdp_ent.t1") || h.opens != 0 {
		t.Errorf("exit %d opens %d stderr %s", code, h.opens, h.stderr.String())
	}
	for _, args := range [][]string{
		{"table", "get"},
		{"table", "get", "a.b.c"},
		{"table", "get", "bdp_ent.t", "oops"},
		{"table", "get", ".t"},
		{"table", "get", "d."},
		{"table", "get", "d.t%"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestRoutesFileIsPlainAndCommittable(t *testing.T) {
	h := twoInstances(t)
	h.mustRun("db", "sync")
	b, err := os.ReadFile(routes.Path(h.dbmetaDir()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "pw") || strings.Contains(string(b), "password") {
		t.Errorf("routes.json must not hold credentials:\n%s", b)
	}
	gi, _ := os.ReadFile(h.dbmetaDir() + "/.gitignore")
	if strings.Contains(string(gi), "routes.json") {
		t.Error("routes.json should not be git-ignored")
	}
}
