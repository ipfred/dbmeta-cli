// Package cli wires commands together. To add a feature, write a *Command
// and register it in newRoot; nothing else needs to change.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"dbmeta-cli/internal/cache"
	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/source"
	"dbmeta-cli/internal/source/mysql"
)

// Version is overridden at build time with -ldflags "-X dbmeta-cli/internal/cli.Version=...".
var Version = "dev"

const binName = "dbmeta-cli"

// Exit codes. Agents rely on these, so keep them stable.
const (
	ExitOK         = 0
	ExitError      = 1
	ExitUsage      = 2 // arguments, identifiers, instance or config problems
	ExitAuth       = 3 // empty password or authentication failure
	ExitNotFound   = 4 // table missing or not visible to the account
	ExitCacheMiss  = 5 // --cached with no cache entry
	ExitNetwork    = 6 // network or connection failure
	ExitNotAllowed = 7 // database outside allow_databases
)

// CodedError carries a specific process exit code.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func usageErr(format string, a ...any) error {
	return &CodedError{Code: ExitUsage, Err: fmt.Errorf(format, a...)}
}

// exitCode maps an error to its process exit code.
func exitCode(err error) int {
	var ce *CodedError
	switch {
	case errors.As(err, &ce):
		return ce.Code
	case errors.Is(err, guard.ErrNotAllowed):
		return ExitNotAllowed
	case errors.Is(err, guard.ErrInvalid), errors.Is(err, config.ErrNotFound):
		return ExitUsage
	case errors.Is(err, source.ErrAuth):
		return ExitAuth
	case errors.Is(err, source.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, cache.ErrMiss):
		return ExitCacheMiss
	case errors.Is(err, source.ErrConn):
		return ExitNetwork
	}
	return ExitError
}

// Env is everything a command touches outside its own arguments, so tests can swap it.
type Env struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Getwd   func() (string, error)
	Getenv  func(string) string
	HomeDir func() (string, error)
	Now     func() time.Time
	// Open connects to an instance. The only implementation reads table structure.
	Open func(ctx context.Context, inst *config.Instance, warn func(string, ...any)) (source.Source, error)
}

func DefaultEnv() *Env {
	return &Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getwd:   os.Getwd,
		Getenv:  os.Getenv,
		HomeDir: os.UserHomeDir,
		Now:     time.Now,
		Open: func(ctx context.Context, inst *config.Instance, warn func(string, ...any)) (source.Source, error) {
			return mysql.Open(ctx, inst, mysql.Options{Warn: warn})
		},
	}
}

func (e *Env) logf(format string, a ...any) {
	fmt.Fprintf(e.Stderr, "dbmeta: "+format+"\n", a...)
}

type Command struct {
	Name    string
	Aliases []string
	Short   string
	Usage   string // shown by -h; group commands get an auto-generated list
	Run     func(ctx context.Context, env *Env, args []string) error
	Sub     []*Command
}

func (c *Command) matches(name string) bool {
	if c.Name == name {
		return true
	}
	for _, a := range c.Aliases {
		if a == name {
			return true
		}
	}
	return false
}

func newRoot() *Command {
	return &Command{
		Name:  binName,
		Short: "只读的数据库表结构查询工具",
		Usage: "用法: dbmeta-cli <命令> [参数]\n\n只取单个表的结构(字段、索引、DDL),带本地缓存。没有执行任意 SQL 的入口。\n请始终使用只读数据库账号。",
		Sub: []*Command{
			initCommand(),
			instanceCommand(),
			tableCommand(),
			cacheCommand(),
			skillCommand(),
			versionCommand(),
		},
	}
}

// Run executes the CLI and returns the process exit code.
func Run(ctx context.Context, env *Env, args []string) int {
	root := newRoot()
	cmd, rest := resolve(root, args)

	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help") {
		printHelp(env.Stdout, cmd)
		return ExitOK
	}
	if cmd.Run == nil {
		if len(rest) > 0 {
			fmt.Fprintf(env.Stderr, "未知命令: %s\n\n", rest[0])
			printHelp(env.Stderr, cmd)
			return ExitUsage
		}
		printHelp(env.Stdout, cmd)
		return ExitOK
	}

	err := cmd.Run(ctx, env, rest)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		printHelp(env.Stdout, cmd)
		return ExitOK
	}
	fmt.Fprintln(env.Stderr, "错误:", err)
	return exitCode(err)
}

func resolve(root *Command, args []string) (*Command, []string) {
	cmd := root
	for len(args) > 0 {
		var next *Command
		for _, s := range cmd.Sub {
			if s.matches(args[0]) {
				next = s
				break
			}
		}
		if next == nil {
			break
		}
		cmd, args = next, args[1:]
	}
	return cmd, args
}

func printHelp(w io.Writer, c *Command) {
	if c.Name == binName {
		fmt.Fprintf(w, "%s (%s)\n\n", c.Short, Version)
	}
	if c.Usage != "" {
		fmt.Fprintln(w, c.Usage)
	}
	if len(c.Sub) == 0 {
		return
	}
	fmt.Fprintln(w, "\n命令:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range c.Sub {
		fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Short)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n使用 \"%s <命令> -h\" 查看详细说明。\n", c.Name)
}

// parseFlags parses flags that may appear before or after positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageErr("%v", err)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

const dirFlagHelp = "dbmeta 目录或项目根目录(默认向上查找 dbmeta/config.json,也可用环境变量 DBMETA_DIR)"

func dirFlag(fs *flag.FlagSet) *string {
	return fs.String("dir", "", dirFlagHelp)
}

// explicitDir is the --dir flag, falling back to DBMETA_DIR.
func explicitDir(env *Env, flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return env.Getenv(config.EnvDir)
}

type project struct {
	dir string // the dbmeta directory
	cfg *config.Config
}

func (p *project) store(env *Env) *cache.Store {
	s := cache.New(p.dir)
	s.Now = env.Now
	return s
}

// findDir locates dbmeta/ without reading the config file.
func findDir(env *Env, flagVal string) (string, error) {
	cwd, err := env.Getwd()
	if err != nil {
		return "", err
	}
	return config.Find(cwd, explicitDir(env, flagVal))
}

func loadProject(env *Env, flagVal string) (*project, error) {
	dir, err := findDir(env, flagVal)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return nil, err
		}
		return nil, &CodedError{Code: ExitUsage, Err: err}
	}
	return &project{dir: dir, cfg: cfg}, nil
}

// displayPath shortens a path relative to the working directory when it is below it.
func displayPath(env *Env, p string) string {
	cwd, err := env.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func noExtraArgs(pos []string) error {
	if len(pos) > 0 {
		return usageErr("多余的参数: %s", strings.Join(pos, " "))
	}
	return nil
}
