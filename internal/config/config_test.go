package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeCfg(t *testing.T, dir, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := ConfigPath(dir)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
}

func TestInitAndRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dbmeta")
	created, err := Init(dir)
	if err != nil || len(created) != 2 {
		t.Fatalf("Init: %v %v", created, err)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(gi) != "config.json\n" {
		t.Errorf("gitignore = %q", gi)
	}
	if again, _ := Init(dir); len(again) != 0 {
		t.Errorf("second Init should create nothing, got %v", again)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Instances["zeta"] = &Instance{Host: "h2", Port: 3306, User: "u"}
	c.Instances["alpha"] = &Instance{Host: "h1", Port: 3307, User: "u", TLS: "false", AllowDatabases: []string{"a"}}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(ConfigPath(dir))
	if strings.Index(string(raw), `"alpha"`) > strings.Index(string(raw), `"zeta"`) {
		t.Error("instances should be sorted by name")
	}
	if !strings.Contains(string(raw), "\n  \"instances\"") {
		t.Errorf("want 2-space indent:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(ConfigPath(dir))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", st.Mode().Perm())
		}
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Instances["alpha"].Port != 3307 || back.Instances["alpha"].AllowDatabases[0] != "a" {
		t.Errorf("round trip lost data: %+v", back.Instances["alpha"])
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"instances":{"a":{"host":"h","user":"u","password":"","pasword":"x"}}}`,
		"bad name":      `{"instances":{"a b":{"host":"h","user":"u","password":""}}}`,
		"no host":       `{"instances":{"a":{"user":"u","password":""}}}`,
		"bad tls":       `{"instances":{"a":{"host":"h","user":"u","password":"","tls":"maybe"}}}`,
		"bad port":      `{"instances":{"a":{"host":"h","port":70000,"user":"u","password":""}}}`,
		"bad ttl":       `{"ttl":"soon","instances":{}}`,
		"bad allow db":  `{"instances":{"a":{"host":"h","user":"u","password":"","allow_databases":["a.b"]}}}`,
		"trailing":      `{"instances":{}} {}`,
		"not json":      `nope`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "dbmeta")
			writeCfg(t, dir, body, 0o600)
			if _, err := Load(dir); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLoadPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions only")
	}
	dir := filepath.Join(t.TempDir(), "dbmeta")
	writeCfg(t, dir, `{"instances":{}}`, 0o644)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("want chmod hint, got %v", err)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dbmeta")
	writeCfg(t, dir, `{"instances":{}}`, 0o600)
	deep := filepath.Join(root, "a", "b", "c")
	os.MkdirAll(deep, 0o755)

	got, err := Find(deep, "")
	if err != nil || !sameFile(t, got, dir) {
		t.Errorf("walk up: %q %v", got, err)
	}
	for _, explicit := range []string{root, dir} {
		got, err = Find(t.TempDir(), explicit)
		if err != nil || got != dir {
			t.Errorf("explicit %q: %q %v", explicit, got, err)
		}
	}
	if _, err := Find(t.TempDir(), filepath.Join(root, "nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := Find(t.TempDir(), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func TestParseTTL(t *testing.T) {
	ok := map[string]time.Duration{"168h": 168 * time.Hour, "7d": 7 * 24 * time.Hour, "30m": 30 * time.Minute, "0": 0, "1.5d": 36 * time.Hour}
	for in, want := range ok {
		got, err := ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "x", "-1h", "-2d", "d"} {
		if _, err := ParseTTL(in); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
	c := &Config{}
	if d, _ := c.EffectiveTTL(); d != DefaultTTL {
		t.Errorf("default ttl = %v", d)
	}
}

func TestInstanceHelpers(t *testing.T) {
	i := &Instance{Host: "h"}
	if i.Addr() != "h:3306" || i.EffectiveTLS() != "preferred" {
		t.Errorf("%s %s", i.Addr(), i.EffectiveTLS())
	}
	if (&Instance{Host: "::1", Port: 1}).Addr() != "[::1]:1" {
		t.Error("ipv6 addr")
	}
}
