package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"dbmeta-cli/internal/cache"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/routes"
	"dbmeta-cli/internal/source"
)

const (
	// maxParallel bounds how many instances are contacted at once.
	maxParallel = 4
	// autoSyncAfter is how long an instance's database list is trusted before a
	// lookup miss may trigger another sync (also throttles failed attempts).
	autoSyncAfter = time.Hour
	syncTimeout   = 30 * time.Second
)

const dbUsage = `用法: dbmeta-cli db <命令>

维护 库 -> 实例 的映射(.dbmeta/routes.json),让 table get 只写 库.表 就能找到实例。

命令:
  sync [实例]    连接实例,读取其中的库名并写入 routes.json(不写实例 = 全部实例)
  list [--db 库] 查看映射(只读本地文件,不联网)`

func dbCommand() *Command {
	return &Command{
		Name:  "db",
		Short: "维护 库 -> 实例 映射",
		Usage: dbUsage,
		Sub: []*Command{
			{Name: "sync", Short: "同步实例里的库名", Usage: syncUsage, Run: runDBSync},
			{Name: "list", Short: "查看库与实例的映射", Usage: listUsage, Run: runDBList},
		},
	}
}

const syncUsage = `用法: dbmeta-cli db sync [<实例>] [--dir <目录>]

连接实例,读取账号可见的库名(只执行一条固定的 information_schema.SCHEMATA 查询,
系统库、不在 allow_databases 内的库会被排除),整体替换该实例在 routes.json 里的记录。

  不写实例   同步全部实例(最多 4 个并发),并清理 config.json 里已不存在的实例
  某实例失败 保留它原有的记录,其余实例照常更新;全部失败才返回错误

新增实例后:在 config.json 填好密码,再运行 dbmeta-cli db sync <实例>。`

const listUsage = `用法: dbmeta-cli db list [--db <库>] [--json] [--dir <目录>]

列出 库 -> 实例 映射,不联网。一个库出现在多个实例里是正常情况(例如测试和生产)。`

type syncResult struct {
	name string
	dbs  []string
	err  error
}

// syncInstances lists the databases of the named instances, at most
// maxParallel at a time. It never writes routes.json.
func syncInstances(ctx context.Context, env *Env, p *project, names []string) []syncResult {
	res := make([]syncResult, len(names))
	parallel(len(names), maxParallel, func(i int) {
		name := names[i]
		res[i] = syncResult{name: name}
		inst := p.cfg.Instances[name]
		if err := requireCredentials(p, name, inst); err != nil {
			res[i].err = err
			return
		}
		ctx, cancel := context.WithTimeout(ctx, syncTimeout)
		defer cancel()
		src, err := env.Open(ctx, inst, env.warn)
		if err != nil {
			res[i].err = err
			return
		}
		defer src.Close()
		all, err := src.ListDatabases(ctx)
		if err != nil {
			res[i].err = err
			return
		}
		for _, db := range all {
			if guard.Database(inst.AllowDatabases, db) == nil {
				res[i].dbs = append(res[i].dbs, db)
			}
		}
	})
	return res
}

// applySync records successes and remembers failed attempts.
func applySync(rt *routes.Routes, res []syncResult, now time.Time) {
	for _, r := range res {
		if r.err != nil {
			rt.MarkAttempt(r.name, now)
			continue
		}
		rt.Replace(r.name, r.dbs, now)
	}
}

// parallel runs fn(0..n-1) with at most limit calls in flight.
func parallel(n, limit int, fn func(i int)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// worstError picks the error that best explains a total failure:
// anything unexpected, then auth, then network, then "not found", then cache miss.
func worstError(errs []error) error {
	rank := func(err error) int {
		switch {
		case errors.Is(err, source.ErrAuth):
			return 4
		case errors.Is(err, source.ErrConn):
			return 3
		case errors.Is(err, source.ErrNotFound):
			return 2
		case errors.Is(err, cache.ErrMiss):
			return 1
		}
		return 5
	}
	var worst error
	for _, e := range errs {
		if e != nil && (worst == nil || rank(e) > rank(worst)) {
			worst = e
		}
	}
	return worst
}

func saveRoutes(p *project, rt *routes.Routes) error {
	if err := routes.Save(p.dir, rt); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", routes.Path(p.dir), err)
	}
	return nil
}

func runDBSync(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("db sync", flag.ContinueOnError)
	dir := dirFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageErr("最多接受一个实例名称\n\n%s", syncUsage)
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	all := len(pos) == 0
	names := p.cfg.Names()
	if !all {
		if _, err := p.cfg.Instance(pos[0]); err != nil {
			return usageErr("%v", err)
		}
		names = pos
	}
	if len(names) == 0 {
		return usageErr("还没有实例,请先用 dbmeta-cli instance add 添加")
	}
	rt, err := routes.Load(p.dir)
	if err != nil {
		return &CodedError{Code: ExitUsage, Err: err}
	}

	res := syncInstances(ctx, env, p, names)
	now := env.Now()
	applySync(rt, res, now)
	if all {
		if removed := rt.Prune(p.cfg.Names()); len(removed) > 0 {
			env.logf("已清理 config.json 中不存在的实例: %s", strings.Join(removed, ", "))
		}
	}

	var errs []error
	for _, r := range res {
		if r.err != nil {
			errs = append(errs, r.err)
			env.logf("警告: 实例 %s 同步失败,保留原有记录: %v", r.name, r.err)
			continue
		}
		fmt.Fprintf(env.Stdout, "%s  %d 个库\n", r.name, len(r.dbs))
	}
	if len(errs) == len(res) {
		return worstError(errs)
	}
	if err := saveRoutes(p, rt); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "已写入 %s(%d 个库,%d 个实例)\n", displayPath(env, routes.Path(p.dir)), len(rt.Databases), len(rt.Instances))
	return nil
}

type dbInstanceRow struct {
	Name string `json:"name"`
	Env  string `json:"env"`
	Desc string `json:"desc"`
}

type dbRow struct {
	Database  string          `json:"database"`
	Instances []dbInstanceRow `json:"instances"`
}

func runDBList(_ context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("db list", flag.ContinueOnError)
	dir := dirFlag(fs)
	only := fs.String("db", "", "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if err := noExtraArgs(pos); err != nil {
		return err
	}
	if *only != "" {
		if err := guard.Ident("库名", *only); err != nil {
			return err
		}
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	rt, err := routes.Load(p.dir)
	if err != nil {
		return &CodedError{Code: ExitUsage, Err: err}
	}

	dbs := make([]string, 0, len(rt.Databases))
	for db := range rt.Databases {
		if *only == "" || db == *only {
			dbs = append(dbs, db)
		}
	}
	sort.Strings(dbs)
	rows := []dbRow{}
	for _, db := range dbs {
		row := dbRow{Database: db, Instances: []dbInstanceRow{}}
		for _, n := range rt.Lookup(db, func(n string) bool { _, ok := p.cfg.Instances[n]; return ok }) {
			i := p.cfg.Instances[n]
			row.Instances = append(row.Instances, dbInstanceRow{Name: n, Env: i.Env, Desc: i.Desc})
		}
		if len(row.Instances) > 0 {
			rows = append(rows, row)
		}
	}

	var unsynced []string
	for _, n := range p.cfg.Names() {
		if rt.SyncedAt(n).IsZero() {
			unsynced = append(unsynced, n)
		}
	}
	if len(unsynced) > 0 {
		env.logf("提示: 这些实例还没同步过库名: %s(运行 dbmeta-cli db sync)", strings.Join(unsynced, ", "))
	}

	if *asJSON {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(env.Stdout, string(b))
		return nil
	}
	if len(rows) == 0 {
		if *only != "" {
			fmt.Fprintf(env.Stdout, "(没有实例包含库 %s)\n", *only)
		} else {
			fmt.Fprintln(env.Stdout, "(还没有映射,运行 dbmeta-cli db sync)")
		}
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "库\t实例(ENV)")
	for _, r := range rows {
		parts := make([]string, 0, len(r.Instances))
		for _, i := range r.Instances {
			if i.Env != "" {
				parts = append(parts, fmt.Sprintf("%s(%s)", i.Name, i.Env))
			} else {
				parts = append(parts, i.Name)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\n", r.Database, strings.Join(parts, ", "))
	}
	return tw.Flush()
}

// autoSyncTargets are the instances worth syncing on a routing miss: they can
// log in and have not been tried within autoSyncAfter.
func autoSyncTargets(p *project, rt *routes.Routes, now time.Time) []string {
	var out []string
	for _, n := range p.cfg.Names() {
		i := p.cfg.Instances[n]
		if i.User == "" || i.Password == "" {
			continue
		}
		if last := rt.LastTried(n); last.IsZero() || now.Sub(last) > autoSyncAfter {
			out = append(out, n)
		}
	}
	return out
}
