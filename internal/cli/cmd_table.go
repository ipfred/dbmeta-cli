package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"dbmeta-cli/internal/cache"
	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/routes"
	"dbmeta-cli/internal/schema"
	"dbmeta-cli/internal/source"
)

const tableUsage = `用法: dbmeta-cli table get <库.表>... [选项]

取表结构(字段、索引、DDL)。一次可以写多个 库.表。stdout 只输出表结构,来源和警告写在 stderr。

不用指定实例:按 .dbmeta/routes.json 找到有这个库的所有实例,逐个取,哪个实例有这张表就输出哪个
(不同实例里的同名表结构相同时,只完整输出一份)。routes.json 里没有这个库时,会先自动同步实例的库名。
库名到实例的映射用 dbmeta-cli db sync 维护。

缓存规则:
  默认(auto)   缓存在有效期内就直接读缓存(不联网、不需要密码);缺失或过期则查库并回写。
               查库失败且有旧缓存时返回旧缓存并警告。实例里确认没有这张表时也会记住,有效期内不再重查。
  --refresh    强制查库并更新缓存;失败就报错,不回退到旧缓存。
  --cached     只读缓存,绝不联网。缺失返回退出码 5;已过期仍返回内容并警告。

选项:
  -i, --instance <实例>  只查这个实例(不依赖 routes.json)
  --refresh          强制实时查库
  --cached           只读缓存
  --ttl <时长>       临时覆盖缓存有效期,例如 24h、7d、0
  --format md|json   输出格式,默认 md;JSON 在只有一个结果时是对象,否则是数组
  -o <文件>          写到指定文件而不是 stdout
  --print-path       只输出缓存文件路径,每行一个(列很多的表可以再用 Read 读文件)
  --dir <目录>       ` + dirFlagHelp + `

部分表取不到时,能取到的照常输出,取不到的写在 stderr,退出码取最严重的一个(3 认证 > 6 网络 > 4 不存在 > 5 无缓存)。`

func tableCommand() *Command {
	return &Command{
		Name:  "table",
		Short: "查看表结构",
		Usage: "用法: dbmeta-cli table get <库.表>... [-i 实例]",
		Sub: []*Command{
			{Name: "get", Short: "获取表结构(可一次多个 库.表)", Usage: tableUsage, Run: runTableGet},
		},
	}
}

type tableRef struct{ db, table string }

func (r tableRef) String() string { return r.db + "." + r.table }

// parseRefs turns "db.table" arguments into validated references, dropping duplicates.
func parseRefs(pos []string) ([]tableRef, error) {
	if len(pos) == 0 {
		return nil, usageErr("需要至少一个 库.表 参数\n\n%s", tableUsage)
	}
	anyDot := false
	for _, a := range pos {
		anyDot = anyDot || strings.Contains(a, ".")
	}
	if !anyDot {
		hint := "bdp_ent.dwd_ent_employee"
		if len(pos) == 3 {
			hint = pos[1] + "." + pos[2]
		}
		return nil, usageErr("参数形式已改为 库.表(不再接受 <实例> <库> <表>),例如: dbmeta-cli table get %s;需要指定实例时加 -i <实例>", hint)
	}
	var refs []tableRef
	seen := map[tableRef]bool{}
	for _, a := range pos {
		db, table, ok := strings.Cut(a, ".")
		if !ok || strings.Contains(table, ".") {
			return nil, usageErr("参数 %q 必须是 库.表 的形式,例如 bdp_ent.dwd_ent_employee", a)
		}
		if err := guard.Ident("库名", db); err != nil {
			return nil, err
		}
		if err := guard.Ident("表名", table); err != nil {
			return nil, err
		}
		r := tableRef{db, table}
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	return refs, nil
}

// planTargets decides which instances to ask for each table.
func planTargets(ctx context.Context, env *Env, p *project, refs []tableRef, pinned string, mode cache.Mode) (map[tableRef][]string, map[tableRef]error, error) {
	targets := map[tableRef][]string{}
	failed := map[tableRef]error{}

	if pinned != "" {
		inst, err := p.cfg.Instance(pinned)
		if err != nil {
			return nil, nil, usageErr("%v", err)
		}
		for _, r := range refs {
			if err := guard.Database(inst.AllowDatabases, r.db); err != nil {
				return nil, nil, err
			}
			targets[r] = []string{pinned}
		}
		return targets, failed, nil
	}

	rt, err := routes.Load(p.dir)
	if err != nil {
		return nil, nil, &CodedError{Code: ExitUsage, Err: err}
	}
	lookup := func(r tableRef) []string {
		return rt.Lookup(r.db, func(n string) bool {
			i, ok := p.cfg.Instances[n]
			return ok && guard.Database(i.AllowDatabases, r.db) == nil
		})
	}
	missing := func() []tableRef {
		var out []tableRef
		for _, r := range refs {
			if len(lookup(r)) == 0 {
				out = append(out, r)
			}
		}
		return out
	}

	if m := missing(); len(m) > 0 && mode != cache.CachedOnly {
		if names := autoSyncTargets(p, rt, env.Now()); len(names) > 0 {
			env.logf("routes.json 里没有库 %s,先同步实例的库名: %s", joinDBs(m), strings.Join(names, ", "))
			res := syncInstances(ctx, env, p, names)
			applySync(rt, res, env.Now())
			for _, r := range res {
				if r.err != nil {
					env.logf("警告: 实例 %s 同步失败: %v", r.name, r.err)
				}
			}
			if err := saveRoutes(p, rt); err != nil {
				env.logf("警告: %v", err)
			}
		}
	}

	for _, r := range refs {
		if names := lookup(r); len(names) > 0 {
			targets[r] = names
			continue
		}
		failed[r] = usageErr("库 %s 没有出现在任何实例里。可用 dbmeta-cli db list 查看已登记的库;库名确认无误时,请用户确认实例是否已登记并填好密码,或用 -i <实例> 直接指定", r.db)
	}
	return targets, failed, nil
}

func joinDBs(refs []tableRef) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range refs {
		if !seen[r.db] {
			seen[r.db] = true
			out = append(out, r.db)
		}
	}
	return strings.Join(out, ", ")
}

type tableResult struct {
	inst string
	out  *cache.Outcome
	err  error
}

func runTableGet(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("table get", flag.ContinueOnError)
	dir := dirFlag(fs)
	refresh := fs.Bool("refresh", false, "")
	cachedOnly := fs.Bool("cached", false, "")
	ttlFlag := fs.String("ttl", "", "")
	format := fs.String("format", "md", "")
	outFile := fs.String("o", "", "")
	printPath := fs.Bool("print-path", false, "")
	var instFlag string
	fs.StringVar(&instFlag, "instance", "", "")
	fs.StringVar(&instFlag, "i", "", "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	refs, err := parseRefs(pos)
	if err != nil {
		return err
	}

	switch {
	case *refresh && *cachedOnly:
		return usageErr("--refresh 和 --cached 不能同时使用")
	case *format != "md" && *format != "json":
		return usageErr("--format 只能是 md 或 json")
	case *printPath && *outFile != "":
		return usageErr("--print-path 和 -o 不能同时使用")
	}
	if instFlag != "" {
		if err := guard.InstanceName(instFlag); err != nil {
			return err
		}
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	ttl, err := p.cfg.EffectiveTTL()
	if err != nil {
		return usageErr("%v", err)
	}
	if *ttlFlag != "" {
		if ttl, err = config.ParseTTL(*ttlFlag); err != nil {
			return usageErr("--ttl: %v", err)
		}
	}
	mode := cache.Auto
	switch {
	case *refresh:
		mode = cache.Refresh
	case *cachedOnly:
		mode = cache.CachedOnly
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	targets, failed, err := planTargets(ctx, env, p, refs, instFlag, mode)
	if err != nil {
		return err
	}
	results := fetchAll(ctx, env, p, refs, targets, mode, ttl)

	// Collect what we got, in the order the tables were asked for.
	var ok []renderItem
	var refErrs []error
	for _, r := range refs {
		if err, bad := failed[r]; bad {
			refErrs = append(refErrs, err)
			continue
		}
		var errs []error
		var got []renderItem
		for _, res := range results[r] {
			label := fmt.Sprintf("%s@%s", r, res.inst)
			if res.err == nil {
				reportSource(env, label, p.cfg.Instances[res.inst], res.out, ttl)
				got = append(got, renderItem{ref: r, inst: res.inst, out: res.out})
				continue
			}
			errs = append(errs, res.err)
			if len(results[r]) > 1 {
				env.logf("%s: 已跳过(%v)", label, res.err)
			}
		}
		ok = append(ok, got...)
		if len(got) == 0 {
			refErrs = append(refErrs, worstError(errs))
		}
	}

	if len(ok) > 0 {
		if err := writeResults(env, ok, *format, *outFile, *printPath); err != nil {
			return err
		}
	}
	switch len(refErrs) {
	case 0:
		return nil
	case 1:
		return refErrs[0]
	}
	for _, e := range refErrs {
		env.logf("错误: %v", e)
	}
	return &CodedError{Code: exitCode(worstError(refErrs)), Err: fmt.Errorf("%d 个表没有取到", len(refErrs))}
}

// fetchAll runs one worker per instance, at most maxParallel at a time. A
// worker opens its connection lazily, so a cache hit never connects, and
// reuses it for every table it has to fetch.
func fetchAll(ctx context.Context, env *Env, p *project, refs []tableRef, targets map[tableRef][]string, mode cache.Mode, ttl time.Duration) map[tableRef][]tableResult {
	byInst := map[string][]tableRef{}
	for _, r := range refs {
		for _, n := range targets[r] {
			byInst[n] = append(byInst[n], r)
		}
	}
	names := make([]string, 0, len(byInst))
	for n := range byInst {
		names = append(names, n)
	}
	sort.Strings(names)

	store := p.store(env)
	type slot struct {
		ref tableRef
		res tableResult
	}
	slots := make([][]slot, len(names))
	parallel(len(names), maxParallel, func(i int) {
		name := names[i]
		inst := p.cfg.Instances[name]
		var src source.Source
		var openErr error
		opened := false
		defer func() {
			if src != nil {
				src.Close()
			}
		}()
		for _, r := range byInst[name] {
			r := r
			fetch := func(ctx context.Context) (*schema.TableSchema, error) {
				if !opened {
					opened = true
					if openErr = requireCredentials(p, name, inst); openErr == nil {
						src, openErr = env.Open(ctx, inst, env.warn)
					}
				}
				if openErr != nil {
					return nil, openErr
				}
				return src.FetchTable(ctx, r.db, r.table)
			}
			out, err := store.Resolve(ctx, cache.Request{
				Instance: name, Database: r.db, Table: r.table, Mode: mode, TTL: ttl,
			}, fetch)
			slots[i] = append(slots[i], slot{r, tableResult{inst: name, out: out, err: err}})
		}
	})

	results := map[tableRef][]tableResult{}
	for _, ss := range slots { // names is sorted, so candidates come out in a stable order
		for _, s := range ss {
			results[s.ref] = append(results[s.ref], s.res)
		}
	}
	return results
}

type renderItem struct {
	ref  tableRef
	inst string
	out  *cache.Outcome
}

// writeResults prints every table that was found. With one result the output
// is exactly the cached document; with several, each one gets a banner line.
func writeResults(env *Env, items []renderItem, format, outFile string, printPath bool) error {
	if printPath {
		for _, it := range items {
			fmt.Fprintln(env.Stdout, it.out.Path)
		}
		return nil
	}

	var body []byte
	switch {
	case format == "json" && len(items) == 1:
		b, err := items[0].out.Schema.JSON()
		if err != nil {
			return err
		}
		body = b
	case format == "json":
		all := make([]*schema.TableSchema, len(items))
		for i, it := range items {
			all[i] = it.out.Schema
		}
		b, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		body = append(b, '\n')
	case len(items) == 1:
		body = []byte(items[0].out.Schema.Markdown())
	default:
		var sb strings.Builder
		first := map[tableRef]renderItem{} // first instance printed in full per table
		for i, it := range items {
			if i > 0 {
				sb.WriteString("\n")
			}
			prev, dup := first[it.ref]
			switch {
			case dup && prev.out.Schema.SchemaHash == it.out.Schema.SchemaHash:
				fmt.Fprintf(&sb, "<!-- %s @ %s: 结构与 %s 相同,不重复输出 -->\n", it.ref, it.inst, prev.inst)
			default:
				if !dup {
					first[it.ref] = it
				}
				fmt.Fprintf(&sb, "<!-- %s @ %s -->\n%s", it.ref, it.inst, it.out.Schema.Markdown())
			}
		}
		body = []byte(sb.String())
	}

	if outFile != "" {
		if err := os.WriteFile(outFile, body, 0o644); err != nil {
			return err
		}
		env.logf("已写入 %s", outFile)
		return nil
	}
	_, err := env.Stdout.Write(body)
	return err
}

// reportSource prints one line to stderr saying where the data came from.
func reportSource(env *Env, label string, inst *config.Instance, out *cache.Outcome, ttl time.Duration) {
	if out.CacheWarn != nil {
		env.logf("%s: 警告: 忽略无法读取的缓存文件: %v", label, out.CacheWarn)
	}
	meta := func() string {
		return fmt.Sprintf("fetched %s, age %s, ttl %s", out.Schema.FetchedAt.Format("2006-01-02"), formatDuration(out.Age), formatDuration(ttl))
	}
	switch out.Kind {
	case cache.Hit:
		env.logf("%s: cache hit (%s)", label, meta())
	case cache.Fetched:
		env.logf("%s: fetched from %s, cache updated", label, inst.Addr())
		if out.Changed {
			env.logf("%s: 表结构已变化(上一次缓存于 %s)", label, out.PreviousAt.Format("2006-01-02 15:04"))
		}
	case cache.StaleCached:
		env.logf("%s: 警告: stale cache, 已过期但按 --cached 不联网 (%s)", label, meta())
	case cache.StaleFallback:
		env.logf("%s: 警告: 查库失败,返回过期缓存 stale (%s): %v", label, meta(), out.FallbackErr)
	}
}

// formatDuration renders ages and TTLs compactly: 7d, 5h, 12m.
func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", d/(24*time.Hour), (d%(24*time.Hour))/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}
