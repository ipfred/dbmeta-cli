package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"dbmeta-cli/internal/cache"
	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/schema"
)

const tableUsage = `用法: dbmeta-cli table get <实例> <库> <表> [选项]

取单个表的结构(字段、索引、DDL)。stdout 只输出表结构,来源和警告写在 stderr。

缓存规则:
  默认(auto)   缓存在有效期内就直接读缓存(不联网、不需要密码);缺失或过期则查库并回写。
               查库失败且有旧缓存时返回旧缓存并警告。
  --refresh    强制查库并更新缓存;失败就报错,不回退到旧缓存。
  --cached     只读缓存,绝不联网。缺失返回退出码 5;已过期仍返回内容并警告。

选项:
  --refresh          强制实时查库
  --cached           只读缓存
  --ttl <时长>       临时覆盖缓存有效期,例如 24h、7d、0
  --format md|json   输出格式,默认 md
  -o <文件>          写到指定文件而不是 stdout
  --print-path       只输出缓存文件路径(列很多的表可以再用 Read 读文件)
  --dir <目录>       ` + dirFlagHelp

func tableCommand() *Command {
	return &Command{
		Name:  "table",
		Short: "查看表结构",
		Usage: "用法: dbmeta-cli table get <实例> <库> <表>",
		Sub: []*Command{
			{Name: "get", Short: "获取单个表的结构", Usage: tableUsage, Run: runTableGet},
		},
	}
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
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 3 {
		return usageErr("需要 3 个参数: <实例> <库> <表>\n\n%s", tableUsage)
	}
	name, db, table := pos[0], pos[1], pos[2]

	switch {
	case *refresh && *cachedOnly:
		return usageErr("--refresh 和 --cached 不能同时使用")
	case *format != "md" && *format != "json":
		return usageErr("--format 只能是 md 或 json")
	case *printPath && *outFile != "":
		return usageErr("--print-path 和 -o 不能同时使用")
	}

	// Identifiers and the allowlist are checked before anything else touches
	// the cache, a password or the network.
	if err := guard.InstanceName(name); err != nil {
		return err
	}
	if err := guard.Ident("库名", db); err != nil {
		return err
	}
	if err := guard.Ident("表名", table); err != nil {
		return err
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	inst, err := p.cfg.Instance(name)
	if err != nil {
		return usageErr("%v", err)
	}
	if err := guard.Database(inst.AllowDatabases, db); err != nil {
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

	fetch := func(ctx context.Context) (*schema.TableSchema, error) {
		if err := requireCredentials(p, name, inst); err != nil {
			return nil, err
		}
		src, err := env.Open(ctx, inst, env.warn)
		if err != nil {
			return nil, err
		}
		defer src.Close()
		return src.FetchTable(ctx, db, table)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := p.store(env).Resolve(ctx, cache.Request{
		Instance: name, Database: db, Table: table, Mode: mode, TTL: ttl,
	}, fetch)
	if err != nil {
		return err
	}
	reportSource(env, inst, out, ttl)

	if *printPath {
		fmt.Fprintln(env.Stdout, out.Path)
		return nil
	}
	var body []byte
	if *format == "json" {
		if body, err = out.Schema.JSON(); err != nil {
			return err
		}
	} else {
		body = []byte(out.Schema.Markdown())
	}
	if *outFile != "" {
		if err := os.WriteFile(*outFile, body, 0o644); err != nil {
			return err
		}
		env.logf("已写入 %s", *outFile)
		return nil
	}
	_, err = env.Stdout.Write(body)
	return err
}

// reportSource prints one line to stderr saying where the data came from.
func reportSource(env *Env, inst *config.Instance, out *cache.Outcome, ttl time.Duration) {
	if out.CacheWarn != nil {
		env.logf("警告: 忽略无法读取的缓存文件: %v", out.CacheWarn)
	}
	meta := func() string {
		return fmt.Sprintf("fetched %s, age %s, ttl %s", out.Schema.FetchedAt.Format("2006-01-02"), formatDuration(out.Age), formatDuration(ttl))
	}
	switch out.Kind {
	case cache.Hit:
		env.logf("cache hit (%s)", meta())
	case cache.Fetched:
		env.logf("fetched from %s, cache updated", inst.Addr())
		if out.Changed {
			env.logf("表结构已变化(上一次缓存于 %s)", out.PreviousAt.Format("2006-01-02 15:04"))
		}
	case cache.StaleCached:
		env.logf("警告: stale cache, 已过期但按 --cached 不联网 (%s)", meta())
	case cache.StaleFallback:
		env.logf("警告: 查库失败,返回过期缓存 stale (%s): %v", meta(), out.FallbackErr)
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
