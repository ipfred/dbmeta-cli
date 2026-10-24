package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/schema"
	"dbmeta-cli/internal/source"
)

type fakeSource struct {
	h *harness
}

func (f *fakeSource) Ping(context.Context) error { return f.h.pingErr }
func (f *fakeSource) Close() error               { return nil }
func (f *fakeSource) FetchTable(_ context.Context, db, table string) (*schema.TableSchema, error) {
	f.h.fetches = append(f.h.fetches, db+"."+table)
	if f.h.fetchErr != nil {
		return nil, f.h.fetchErr
	}
	return &schema.TableSchema{
		Server: "10.0.0.12:3306", ServerVersion: "8.0.32", Engine: "InnoDB", Charset: "utf8mb4", RowsEstimate: 10,
		Comment: f.h.comment,
		Columns: []schema.Column{{Ordinal: 1, Name: "id", Type: "bigint", Key: "PRI"}, {Ordinal: 2, Name: f.h.extraCol, Type: "int"}},
		Indexes: []schema.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}, Type: "BTREE"}},
		DDL:     "CREATE TABLE `" + table + "` (...)",
	}, nil
}

type harness struct {
	t        *testing.T
	cwd      string
	home     string
	now      time.Time
	env      *Env
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	opens    int
	fetches  []string
	fetchErr error
	pingErr  error
	openErr  error
	comment  string
	extraCol string
	environ  map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	h := &harness{t: t, cwd: filepath.Join(root, "proj"), home: filepath.Join(root, "home"),
		now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), extraCol: "status", comment: "orders", environ: map[string]string{}}
	os.MkdirAll(h.cwd, 0o755)
	os.MkdirAll(h.home, 0o755)
	h.env = &Env{
		Stdout: &h.stdout, Stderr: &h.stderr,
		Getwd:   func() (string, error) { return h.cwd, nil },
		Getenv:  func(k string) string { return h.environ[k] },
		HomeDir: func() (string, error) { return h.home, nil },
		Now:     func() time.Time { return h.now },
		Open: func(_ context.Context, inst *config.Instance, _ func(string, ...any)) (source.Source, error) {
			h.opens++
			if h.openErr != nil {
				return nil, h.openErr
			}
			return &fakeSource{h}, nil
		},
	}
	return h
}

// run executes the CLI and returns the exit code, resetting captured output first.
func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return Run(context.Background(), h.env, args)
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	if code := h.run(args...); code != 0 {
		h.t.Fatalf("%v exited %d\nstdout: %s\nstderr: %s", args, code, h.stdout.String(), h.stderr.String())
	}
	return h.stdout.String()
}

func (h *harness) dbmetaDir() string { return filepath.Join(h.cwd, "dbmeta") }

// setPassword edits config.json the way a user would.
func (h *harness) setPassword(name, pw string) {
	h.t.Helper()
	c, err := config.Load(h.dbmetaDir())
	if err != nil {
		h.t.Fatal(err)
	}
	c.Instances[name].Password = pw
	if err := config.Save(h.dbmetaDir(), c); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) addOrder(extra ...string) {
	h.t.Helper()
	args := append([]string{"instance", "add", "order_prod", "--jdbc", "jdbc:mysql://10.0.0.12:3306/order?user=readonly&useSSL=false",
		"--env", "prod", "--desc", "订单主库"}, extra...)
	h.mustRun(args...)
	h.setPassword("order_prod", "pw")
}

func TestInitCreatesFiles(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("init")
	if !strings.Contains(out, "只读") {
		t.Errorf("init should remind about read-only accounts:\n%s", out)
	}
	gi, _ := os.ReadFile(filepath.Join(h.dbmetaDir(), ".gitignore"))
	if string(gi) != "config.json\n" {
		t.Errorf(".gitignore = %q", gi)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(h.dbmetaDir(), "config.json"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("config.json mode %v", st.Mode().Perm())
		}
	}
	// Idempotent.
	if out := h.mustRun("init"); !strings.Contains(out, "无需改动") {
		t.Errorf("second init: %s", out)
	}
	if h.run("init", "extra") != ExitUsage {
		t.Error("extra args should be a usage error")
	}
}

func TestInstanceAddJDBC(t *testing.T) {
	h := newHarness(t)
	// dbmeta/ does not exist yet: add must init it.
	out := h.mustRun("instance", "add", "order_prod", "--env", "prod", "--desc", "订单主库",
		"--jdbc", "jdbc:mysql://10.0.0.12:3306/order?user=readonly&password=SECRET&useSSL=false&serverTimezone=UTC")
	for _, want := range []string{
		"已初始化", "instances.order_prod", "10.0.0.12:3306", "user=readonly", "tls=false",
		"password 为空", "默认库 order 未写入白名单", "已忽略的参数: serverTimezone", "密码",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Error("password leaked in output")
	}
	raw, _ := os.ReadFile(filepath.Join(h.dbmetaDir(), "config.json"))
	if strings.Contains(string(raw), "SECRET") {
		t.Error("password from JDBC URL was written to config.json")
	}
	c, err := config.Load(h.dbmetaDir())
	if err != nil {
		t.Fatal(err)
	}
	i := c.Instances["order_prod"]
	if i.Host != "10.0.0.12" || i.Port != 3306 || i.User != "readonly" || i.TLS != "false" || i.Env != "prod" || i.Password != "" || len(i.AllowDatabases) != 0 {
		t.Errorf("%+v", i)
	}
}

func TestInstanceAddRules(t *testing.T) {
	h := newHarness(t)
	h.mustRun("instance", "add", "a", "--host", "h1", "--user", "u", "--port", "3307", "--allow-db", "x, y", "--tls", "skip-verify")
	c, _ := config.Load(h.dbmetaDir())
	if i := c.Instances["a"]; i.Port != 3307 || i.TLS != "skip-verify" || strings.Join(i.AllowDatabases, ",") != "x,y" {
		t.Errorf("%+v", i)
	}
	if h.run("instance", "add", "a", "--host", "h2") != ExitUsage {
		t.Error("duplicate without --force must fail")
	}
	h.setPassword("a", "keepme")
	// --force with the same target keeps the password.
	h.mustRun("instance", "add", "a", "--host", "h1", "--user", "u", "--port", "3307", "--force")
	if c, _ := config.Load(h.dbmetaDir()); c.Instances["a"].Password != "keepme" {
		t.Error("password should survive --force when host/port/user are unchanged")
	}
	// --force pointing elsewhere drops it.
	h.mustRun("instance", "add", "a", "--host", "other", "--user", "u", "--force")
	if c, _ := config.Load(h.dbmetaDir()); c.Instances["a"].Password != "" {
		t.Error("password must not follow a changed host")
	}

	bad := [][]string{
		{"instance", "add"},
		{"instance", "add", "bad name", "--host", "h"},
		{"instance", "add", "x"},
		{"instance", "add", "x", "--host", "h", "--jdbc", "jdbc:mysql://h/d"},
		{"instance", "add", "x", "--jdbc", "jdbc:postgresql://h/d"},
		{"instance", "add", "x", "--host", "h", "--tls", "maybe"},
		{"instance", "add", "x", "--host", "h", "--allow-db", "a.b"},
		{"instance", "add", "x", "--host", "h", "--port", "99999"},
	}
	for _, args := range bad {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, h.stderr.String())
		}
	}
}

func TestInstanceList(t *testing.T) {
	h := newHarness(t)
	h.addOrder("--allow-db", "order,order_history")

	out := h.mustRun("instance", "list")
	for _, want := range []string{"order_prod", "10.0.0.12:3306", "readonly", "prod", "订单主库", "order,order_history", "已填写", "0 库 / 0 表"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pw") && strings.Contains(strings.ReplaceAll(out, "已填写", ""), "pw") {
		t.Errorf("list must not show the password:\n%s", out)
	}

	h.mustRun("table", "get", "order_prod", "order", "t_order")
	if out := h.mustRun("instance", "list"); !strings.Contains(out, "1 库 / 1 表") {
		t.Errorf("cache counts missing:\n%s", out)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("instance", "list", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["password_set"] != true || rows[0]["cached_tables"].(float64) != 1 {
		t.Errorf("%v", rows)
	}
	if _, has := rows[0]["password"]; has {
		t.Error("json must not contain a password field")
	}
}

func TestListWithoutInit(t *testing.T) {
	h := newHarness(t)
	if code := h.run("instance", "list"); code != ExitUsage || !strings.Contains(h.stderr.String(), "未找到 dbmeta/config.json") {
		t.Errorf("code %d: %s", code, h.stderr.String())
	}
	if code := h.run("table", "get", "a", "d", "t"); code != ExitUsage || !strings.Contains(h.stderr.String(), "dbmeta-cli init") {
		t.Errorf("code %d: %s", code, h.stderr.String())
	}
}

func TestConfigDiscoveryFromSubdirAndEnv(t *testing.T) {
	h := newHarness(t)
	h.addOrder()
	sub := filepath.Join(h.cwd, "src", "deep")
	os.MkdirAll(sub, 0o755)
	h.cwd = sub
	if out := h.mustRun("instance", "list"); !strings.Contains(out, "order_prod") {
		t.Errorf("walk-up discovery failed:\n%s", out)
	}
	h.cwd = t.TempDir()
	if h.run("instance", "list") != ExitUsage {
		t.Error("should not find config outside the project")
	}
	h.environ["DBMETA_DIR"] = filepath.Join(sub, "..", "..")
	if out := h.mustRun("instance", "list"); !strings.Contains(out, "order_prod") {
		t.Errorf("DBMETA_DIR failed:\n%s", out)
	}
}

func TestTableGetCacheFlow(t *testing.T) {
	h := newHarness(t)
	h.addOrder()

	// 1. First call queries and writes the cache; stdout is only the structure.
	out := h.mustRun("table", "get", "order_prod", "order", "t_order")
	if h.opens != 1 || len(h.fetches) != 1 {
		t.Fatalf("opens=%d fetches=%v", h.opens, h.fetches)
	}
	if !strings.HasPrefix(out, "---\ninstance: order_prod") || !strings.Contains(out, "| status | int |") {
		t.Errorf("unexpected stdout:\n%s", out)
	}
	if !strings.Contains(h.stderr.String(), "fetched from 10.0.0.12:3306") || strings.Contains(out, "dbmeta:") {
		t.Errorf("source line belongs on stderr only: %q", h.stderr.String())
	}
	cacheFile := filepath.Join(h.dbmetaDir(), "order_prod", "order", "t_order.md")
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatal(err)
	}

	// 2. Within TTL: cache hit, no connection.
	h.now = h.now.Add(3 * 24 * time.Hour)
	h.opens = 0
	again := h.mustRun("table", "get", "order_prod", "order", "t_order")
	if h.opens != 0 || again == "" {
		t.Errorf("cache hit should not connect (opens=%d)", h.opens)
	}
	if want := "dbmeta: cache hit (fetched 2026-10-24, age 3d, ttl 7d)"; !strings.Contains(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", h.stderr.String(), want)
	}
	if strings.Replace(again, "stale: false", "", 1) != strings.Replace(out, "stale: false", "", 1) {
		t.Error("cached output differs from the fresh output")
	}

	// 3. --ttl 1d makes it expired -> refetch, and a changed table is reported.
	h.extraCol = "state"
	h.mustRun("table", "get", "order_prod", "order", "t_order", "--ttl", "1d")
	if h.opens != 1 || !strings.Contains(h.stderr.String(), "表结构已变化") {
		t.Errorf("opens=%d stderr=%q", h.opens, h.stderr.String())
	}
	b, _ := os.ReadFile(cacheFile)
	if !strings.Contains(string(b), "changed_at:") || !strings.Contains(string(b), "| state | int |") {
		t.Errorf("cache not updated:\n%s", b)
	}

	// 4. --refresh always connects, even when fresh.
	h.opens = 0
	h.mustRun("table", "get", "order_prod", "order", "t_order", "--refresh")
	if h.opens != 1 {
		t.Errorf("--refresh opens=%d", h.opens)
	}

	// 5. --cached never connects, even when expired; warns instead.
	h.opens = 0
	h.now = h.now.Add(30 * 24 * time.Hour)
	h.mustRun("table", "get", "order_prod", "order", "t_order", "--cached")
	if h.opens != 0 || !strings.Contains(h.stderr.String(), "stale") {
		t.Errorf("opens=%d stderr=%q", h.opens, h.stderr.String())
	}

	// 6. Expired + database down: stale fallback with exit 0 and a warning.
	h.openErr = fmt.Errorf("%w: dial tcp: refused", source.ErrConn)
	h.mustRun("table", "get", "order_prod", "order", "t_order")
	if !strings.Contains(h.stderr.String(), "警告") || !strings.Contains(h.stderr.String(), "stale") {
		t.Errorf("fallback warning missing: %q", h.stderr.String())
	}
	if b, _ := os.ReadFile(cacheFile); !strings.Contains(string(b), "stale: true") {
		t.Error("cache file should be marked stale")
	}

	// 7. --refresh with the database down fails with 6 and no fallback.
	if code := h.run("table", "get", "order_prod", "order", "t_order", "--refresh"); code != ExitNetwork {
		t.Errorf("--refresh with db down: exit %d", code)
	}
	if h.stdout.Len() != 0 {
		t.Error("nothing should reach stdout on failure")
	}
}

func TestTableGetOutputModes(t *testing.T) {
	h := newHarness(t)
	h.addOrder()

	var ts schema.TableSchema
	if err := json.Unmarshal([]byte(h.mustRun("table", "get", "order_prod", "order", "t_order", "--format", "json")), &ts); err != nil {
		t.Fatal(err)
	}
	if ts.Table != "t_order" || len(ts.Columns) != 2 || ts.SchemaHash == "" {
		t.Errorf("%+v", ts)
	}

	path := strings.TrimSpace(h.mustRun("table", "get", "order_prod", "order", "t_order", "--print-path"))
	if path != filepath.Join(h.dbmetaDir(), "order_prod", "order", "t_order.md") {
		t.Errorf("print-path = %q", path)
	}

	out := filepath.Join(t.TempDir(), "out.md")
	if got := h.mustRun("table", "get", "order_prod", "order", "t_order", "-o", out); got != "" {
		t.Errorf("-o should leave stdout empty, got %q", got)
	}
	if b, err := os.ReadFile(out); err != nil || !strings.Contains(string(b), "# order_prod / order / t_order") {
		t.Errorf("%v %s", err, b)
	}
}

func TestTableGetExitCodes(t *testing.T) {
	h := newHarness(t)
	h.addOrder("--allow-db", "order")

	cases := []struct {
		name  string
		args  []string
		pre   func()
		code  int
		opens int
	}{
		{"unknown instance", []string{"nope", "order", "t"}, nil, ExitUsage, 0},
		{"bad db identifier", []string{"order_prod", "or`der", "t"}, nil, ExitUsage, 0},
		{"wildcard table", []string{"order_prod", "order", "t%"}, nil, ExitUsage, 0},
		{"traversal table", []string{"order_prod", "order", "../x"}, nil, ExitUsage, 0},
		{"dotted name", []string{"order_prod", "order", "a.b"}, nil, ExitUsage, 0},
		{"missing args", []string{"order_prod", "order"}, nil, ExitUsage, 0},
		{"conflicting flags", []string{"order_prod", "order", "t", "--refresh", "--cached"}, nil, ExitUsage, 0},
		{"bad format", []string{"order_prod", "order", "t", "--format", "xml"}, nil, ExitUsage, 0},
		{"bad ttl", []string{"order_prod", "order", "t", "--ttl", "soon"}, nil, ExitUsage, 0},
		{"outside allowlist", []string{"order_prod", "other_db", "t"}, nil, ExitNotAllowed, 0},
		{"allowlist applies to --cached", []string{"order_prod", "other_db", "t", "--cached"}, nil, ExitNotAllowed, 0},
		{"cache miss", []string{"order_prod", "order", "t", "--cached"}, nil, ExitCacheMiss, 0},
		{"table not found", []string{"order_prod", "order", "t"}, func() { h.fetchErr = fmt.Errorf("%w: order.t", source.ErrNotFound) }, ExitNotFound, 1},
		{"auth failure", []string{"order_prod", "order", "t"}, func() { h.fetchErr = fmt.Errorf("%w: denied", source.ErrAuth) }, ExitAuth, 1},
		{"network failure", []string{"order_prod", "order", "t"}, func() { h.openErr = fmt.Errorf("%w: refused", source.ErrConn) }, ExitNetwork, 1},
		{"empty password", []string{"order_prod", "order", "t"}, func() { h.setPassword("order_prod", "") }, ExitAuth, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.fetchErr, h.openErr, h.opens = nil, nil, 0
			h.setPassword("order_prod", "pw")
			if tc.pre != nil {
				tc.pre()
			}
			if code := h.run(append([]string{"table", "get"}, tc.args...)...); code != tc.code {
				t.Errorf("exit %d, want %d\nstderr: %s", code, tc.code, h.stderr.String())
			}
			if h.opens != tc.opens {
				t.Errorf("opens = %d, want %d", h.opens, tc.opens)
			}
			if h.stdout.Len() != 0 {
				t.Errorf("stdout should be empty on failure: %q", h.stdout.String())
			}
		})
	}
}

func TestEmptyPasswordErrorMentionsConfigButNotSecrets(t *testing.T) {
	h := newHarness(t)
	h.mustRun("instance", "add", "a", "--host", "h", "--user", "u")
	if code := h.run("table", "get", "a", "d", "t"); code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.stderr.String(), "config.json") || h.opens != 0 {
		t.Errorf("opens=%d stderr=%s", h.opens, h.stderr.String())
	}
	if code := h.run("instance", "ping", "a"); code != ExitAuth || h.opens != 0 {
		t.Errorf("ping with empty password: exit %d opens %d", code, h.opens)
	}
}

func TestEmptyPasswordWithStaleCacheFallsBack(t *testing.T) {
	h := newHarness(t)
	h.addOrder()
	h.mustRun("table", "get", "order_prod", "order", "t_order")
	h.setPassword("order_prod", "")
	h.now = h.now.Add(10 * 24 * time.Hour)
	h.mustRun("table", "get", "order_prod", "order", "t_order")
	if !strings.Contains(h.stderr.String(), "stale") {
		t.Errorf("stderr: %s", h.stderr.String())
	}
}

func TestInstancePing(t *testing.T) {
	h := newHarness(t)
	h.addOrder()
	if out := h.mustRun("instance", "ping", "order_prod"); !strings.Contains(out, "OK") {
		t.Errorf("%s", out)
	}
	h.pingErr = fmt.Errorf("%w: access denied", source.ErrAuth)
	if code := h.run("instance", "ping", "order_prod"); code != ExitAuth {
		t.Errorf("exit %d", code)
	}
	if h.run("instance", "ping", "ghost") != ExitUsage || h.run("instance", "ping") != ExitUsage {
		t.Error("unknown or missing instance should be a usage error")
	}
}

func TestCacheCommands(t *testing.T) {
	h := newHarness(t)
	h.addOrder()
	h.mustRun("table", "get", "order_prod", "order", "t1")
	h.mustRun("table", "get", "order_prod", "order", "t2")
	h.now = h.now.Add(8 * 24 * time.Hour)

	out := h.mustRun("cache", "list")
	if !strings.Contains(out, "t1") || !strings.Contains(out, "t2") || !strings.Contains(out, "已过期") || !strings.Contains(out, "8d") {
		t.Errorf("%s", out)
	}
	if out := h.mustRun("cache", "list", "order_prod", "nodb"); !strings.Contains(out, "没有缓存") {
		t.Errorf("%s", out)
	}

	if out := h.mustRun("cache", "clear", "order_prod", "order", "t1"); !strings.Contains(out, "1 个") {
		t.Errorf("%s", out)
	}
	if code := h.run("table", "get", "order_prod", "order", "t1", "--cached"); code != ExitCacheMiss {
		t.Errorf("cleared entry should miss, exit %d", code)
	}
	if out := h.mustRun("cache", "clear", "order_prod"); !strings.Contains(out, "1 个") {
		t.Errorf("%s", out)
	}
	for _, args := range [][]string{{"cache", "clear"}, {"cache", "clear", ".."}, {"cache", "clear", "order_prod", "a/b"}, {"cache", "clear", "order_prod", "d", "../t"}} {
		if h.run(args...) != ExitUsage {
			t.Errorf("%v should be a usage error", args)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dbmetaDir(), "config.json")); err != nil {
		t.Error("cache clear must never touch config.json")
	}
}

func TestSkillInstall(t *testing.T) {
	h := newHarness(t)

	// Default: global, agents.
	h.mustRun("skill", "install")
	global := filepath.Join(h.home, ".agents", "skills", "dbmeta", "SKILL.md")
	b, err := os.ReadFile(global)
	if err != nil || !strings.HasPrefix(string(b), "---\nname: dbmeta\n") {
		t.Fatalf("%v\n%s", err, b)
	}
	if strings.Contains(string(b), "password\":") {
		t.Error("skill must not contain connection info")
	}

	// Project scope: next to dbmeta/'s parent, found from a subdirectory.
	h.mustRun("init")
	projRoot := h.cwd
	sub := filepath.Join(h.cwd, "pkg")
	os.MkdirAll(sub, 0o755)
	h.cwd = sub
	h.mustRun("skill", "install", "--scope", "project", "--target", "agents,claude")
	for _, p := range []string{".agents/skills/dbmeta/SKILL.md", ".claude/skills/dbmeta/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(projRoot, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}

	// No dbmeta/ at all: project root is the working directory.
	h.cwd = t.TempDir()
	h.mustRun("skill", "install", "--scope", "project")
	if _, err := os.Stat(filepath.Join(h.cwd, ".agents", "skills", "dbmeta", "SKILL.md")); err != nil {
		t.Error(err)
	}

	// --dir wins over --scope/--target.
	custom := filepath.Join(t.TempDir(), "skills")
	h.mustRun("skill", "install", "--dir", custom, "--scope", "project", "--target", "claude")
	if _, err := os.Stat(filepath.Join(custom, "dbmeta", "SKILL.md")); err != nil {
		t.Error(err)
	}

	// All targets, then uninstall.
	h.mustRun("skill", "install", "--target", "all")
	for _, rel := range []string{".claude/skills", ".factory/skills", ".codex/skills", ".config/opencode/skills"} {
		if _, err := os.Stat(filepath.Join(h.home, rel, "dbmeta", "SKILL.md")); err != nil {
			t.Errorf("target %s: %v", rel, err)
		}
	}
	out := h.mustRun("skill", "uninstall", "--target", "all")
	if !strings.Contains(out, "已卸载") {
		t.Errorf("%s", out)
	}
	if _, err := os.Stat(global); !os.IsNotExist(err) {
		t.Error("uninstall should remove the skill")
	}
	if out := h.mustRun("skill", "uninstall"); !strings.Contains(out, "跳过") {
		t.Errorf("uninstalling twice should skip: %s", out)
	}

	for _, args := range [][]string{{"skill", "install", "--scope", "galaxy"}, {"skill", "install", "--target", "vim"}, {"skill", "install", "extra"}} {
		if h.run(args...) != ExitUsage {
			t.Errorf("%v should be a usage error", args)
		}
	}
	if out := h.mustRun("skill", "print"); !strings.Contains(out, "name: dbmeta") {
		t.Errorf("print: %s", out)
	}
}

func TestVersionAndHelp(t *testing.T) {
	h := newHarness(t)
	if out := h.mustRun("version"); !strings.HasPrefix(out, "dbmeta-cli ") {
		t.Errorf("%s", out)
	}
	if out := h.mustRun("-h"); !strings.Contains(out, "instance") || !strings.Contains(out, "table") {
		t.Errorf("%s", out)
	}
	if out := h.mustRun("table", "get", "-h"); !strings.Contains(out, "--refresh") {
		t.Errorf("%s", out)
	}
	if h.run("bogus") != ExitUsage {
		t.Error("unknown command should be a usage error")
	}
	for _, banned := range []string{"sql", "exec", "query", "databases", "tables"} {
		if code := h.run(banned); code != ExitUsage {
			t.Errorf("command %q must not exist (exit %d)", banned, code)
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := map[error]int{
		errors.New("x"):                            ExitError,
		fmt.Errorf("w: %w", source.ErrAuth):        ExitAuth,
		fmt.Errorf("w: %w", source.ErrNotFound):    ExitNotFound,
		fmt.Errorf("w: %w", source.ErrConn):        ExitNetwork,
		fmt.Errorf("w: %w", config.ErrNotFound):    ExitUsage,
		&CodedError{Code: 42, Err: errors.New("")}: 42,
	}
	for err, want := range cases {
		if got := exitCode(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}
