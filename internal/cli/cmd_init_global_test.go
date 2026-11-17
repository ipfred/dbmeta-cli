package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (h *harness) globalDir() string { return filepath.Join(h.home, ".dbmeta") }

func TestInitGlobalCreatesUserLevelConfig(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("init", "--scope", "global")
	if _, err := os.Stat(filepath.Join(h.globalDir(), "config.json")); err != nil {
		t.Fatalf("global config missing: %v", err)
	}
	if _, err := os.Stat(h.dbmetaDir()); err == nil {
		t.Error("--scope global must not create a project .dbmeta/")
	}
	if !strings.Contains(out, h.globalDir()) || !strings.Contains(out, "没有自己 .dbmeta/ 的项目") {
		t.Errorf("output should say where the global dir is:\n%s", out)
	}
	if out := h.mustRun("init", "--scope", "global"); !strings.Contains(out, "无需改动") {
		t.Errorf("second global init: %s", out)
	}
	gi, _ := os.ReadFile(filepath.Join(h.globalDir(), ".gitignore"))
	if string(gi) != "config.json\n" {
		t.Errorf(".gitignore = %q", gi)
	}
}

func TestInitScopeValidation(t *testing.T) {
	h := newHarness(t)
	if code := h.run("init", "--scope", "global", "--dir", h.cwd); code != ExitUsage {
		t.Errorf("--scope global with --dir: exit %d", code)
	}
	if code := h.run("init", "--scope", "everywhere"); code != ExitUsage {
		t.Errorf("bad scope: exit %d", code)
	}
	h.env.HomeDir = func() (string, error) { return "", errors.New("no home") }
	if code := h.run("init", "--scope", "global"); code == ExitOK {
		t.Error("global init without a home directory must fail")
	}
}

func TestGlobalConfigServesProjectsWithoutTheirOwn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "--scope", "global")

	// From a project that has no .dbmeta/ of its own, add writes to the global config.
	h.mustRun("instance", "add", "shared", "--host", "10.0.0.9", "--user", "ro")
	if _, err := os.Stat(h.dbmetaDir()); err == nil {
		t.Fatal("instance add created a project .dbmeta/ even though a global one exists")
	}
	other := filepath.Join(filepath.Dir(h.cwd), "another")
	os.MkdirAll(other, 0o755)
	h.cwd = other
	if out := h.mustRun("instance", "list"); !strings.Contains(out, "shared") {
		t.Errorf("another project should see the global instance:\n%s", out)
	}

	// A project .dbmeta/ takes over completely.
	h.mustRun("init")
	h.mustRun("instance", "add", "local", "--host", "10.0.0.8", "--user", "ro")
	out := h.mustRun("instance", "list")
	if !strings.Contains(out, "local") || strings.Contains(out, "shared") {
		t.Errorf("project config should win over global:\n%s", out)
	}

	// An explicit directory never falls back to the global one.
	h.cwd = t.TempDir()
	h.environ["DBMETA_DIR"] = filepath.Join(h.cwd, "missing")
	if code := h.run("instance", "list"); code != ExitUsage {
		t.Errorf("DBMETA_DIR pointing nowhere: exit %d", code)
	}
}

func TestGlobalConfigIsNotAProjectRootForSkills(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "--scope", "global")
	h.mustRun("skill", "install", "--scope", "project", "--target", "agents")
	if _, err := os.Stat(filepath.Join(h.cwd, ".agents", "skills", "dbmeta", "SKILL.md")); err != nil {
		t.Errorf("project skill should land in the working directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.home, ".agents")); err == nil {
		t.Error("project-scope skill must not be written into the home directory")
	}
}

func TestGlobalConfigWhenCwdIsUnderHome(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "--scope", "global")
	h.cwd = filepath.Join(h.home, "work", "proj") // the ordinary walk-up already reaches ~/.dbmeta
	os.MkdirAll(h.cwd, 0o755)
	h.mustRun("instance", "add", "shared", "--host", "10.0.0.9", "--user", "ro")
	if out := h.mustRun("instance", "list"); !strings.Contains(out, "shared") {
		t.Errorf("%s", out)
	}
	h.mustRun("skill", "install", "--scope", "project", "--target", "agents")
	if _, err := os.Stat(filepath.Join(h.cwd, ".agents", "skills", "dbmeta", "SKILL.md")); err != nil {
		t.Errorf("a project skill must go to the project, not ~/: %v", err)
	}
}
