package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/skills"
)

// agentTarget is an AI client that loads skills from a directory. The paths
// are relative to the home directory (global) or the project root (project).
type agentTarget struct {
	name       string
	globalRel  string
	projectRel string
}

var agentTargets = []agentTarget{
	{"agents", filepath.Join(".agents", "skills"), filepath.Join(".agents", "skills")},
	{"claude", filepath.Join(".claude", "skills"), filepath.Join(".claude", "skills")},
	{"factory", filepath.Join(".factory", "skills"), filepath.Join(".factory", "skills")},
	{"codex", filepath.Join(".codex", "skills"), filepath.Join(".codex", "skills")},
	{"opencode", filepath.Join(".config", "opencode", "skills"), filepath.Join(".opencode", "skills")},
}

const skillUsage = `用法: dbmeta-cli skill <命令> [选项]

命令:
  install     安装 dbmeta skill
  uninstall   卸载
  print       把 SKILL.md 输出到 stdout

选项(install / uninstall):
  --scope global|project   默认 global。project 装到 <项目根>/ 下(项目根是 dbmeta/ 的上一级,找不到则取当前目录)
  --target <列表>          agents,claude,factory,codex,opencode,all(逗号分隔),默认 agents
  --dir <目录>             安装到自定义 skills 目录(优先于 --target 和 --scope)

位置示例(target=agents):
  global   ~/.agents/skills/dbmeta/SKILL.md
  project  <项目根>/.agents/skills/dbmeta/SKILL.md`

func skillCommand() *Command {
	return &Command{
		Name:  "skill",
		Short: "安装/卸载 AI agent 使用的 skill",
		Usage: skillUsage,
		Sub: []*Command{
			{Name: "install", Short: "安装 skill", Usage: skillUsage, Run: func(_ context.Context, env *Env, args []string) error {
				return runSkill(env, args, true)
			}},
			{Name: "uninstall", Short: "卸载 skill", Usage: skillUsage, Run: func(_ context.Context, env *Env, args []string) error {
				return runSkill(env, args, false)
			}},
			{Name: "print", Short: "输出 SKILL.md", Usage: "用法: dbmeta-cli skill print", Run: func(_ context.Context, env *Env, _ []string) error {
				b, err := skills.FS.ReadFile(skills.Name + "/SKILL.md")
				if err != nil {
					return err
				}
				_, err = env.Stdout.Write(b)
				return err
			}},
		},
	}
}

func runSkill(env *Env, args []string, install bool) error {
	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	scope := fs.String("scope", "global", "")
	target := fs.String("target", "agents", "")
	dir := fs.String("dir", "", "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if err := noExtraArgs(pos); err != nil {
		return err
	}
	dirs, err := resolveSkillDirs(env, *scope, *target, *dir)
	if err != nil {
		return err
	}

	content, err := skills.FS.ReadFile(skills.Name + "/SKILL.md")
	if err != nil {
		return err
	}
	for _, d := range dirs {
		dest := filepath.Join(d.path, skills.Name)
		if install {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), content, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(env.Stdout, "已安装 [%s] %s\n", d.name, dest)
			continue
		}
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			fmt.Fprintf(env.Stdout, "跳过 [%s] 未安装\n", d.name)
			continue
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "已卸载 [%s] %s\n", d.name, dest)
	}
	return nil
}

type skillDir struct{ name, path string }

func resolveSkillDirs(env *Env, scope, target, dir string) ([]skillDir, error) {
	if dir != "" {
		return []skillDir{{"custom", dir}}, nil
	}
	if scope != "global" && scope != "project" {
		return nil, usageErr("--scope 只能是 global 或 project")
	}

	wanted := map[string]bool{}
	for _, t := range strings.Split(target, ",") {
		if t = strings.TrimSpace(t); t != "" {
			wanted[t] = true
		}
	}
	for t := range wanted {
		known := t == "all"
		for _, a := range agentTargets {
			known = known || a.name == t
		}
		if !known {
			return nil, usageErr("未知的 target %q,可选: agents, claude, factory, codex, opencode, all", t)
		}
	}
	if len(wanted) == 0 {
		return nil, usageErr("--target 不能为空")
	}

	var base string
	if scope == "global" {
		home, err := env.HomeDir()
		if err != nil {
			return nil, err
		}
		base = home
	} else {
		root, err := projectRoot(env)
		if err != nil {
			return nil, err
		}
		base = root
	}

	var out []skillDir
	for _, a := range agentTargets {
		if !wanted["all"] && !wanted[a.name] {
			continue
		}
		rel := a.globalRel
		if scope == "project" {
			rel = a.projectRel
		}
		out = append(out, skillDir{a.name, filepath.Join(base, rel)})
	}
	return out, nil
}

// projectRoot is the parent of dbmeta/, or the working directory when there is none.
func projectRoot(env *Env) (string, error) {
	cwd, err := env.Getwd()
	if err != nil {
		return "", err
	}
	if dir, err := config.Find(cwd, env.Getenv(config.EnvDir)); err == nil {
		return filepath.Dir(dir), nil
	}
	return cwd, nil
}
