package cli

import (
	"context"
	"flag"
	"fmt"
	"text/tabwriter"

	"dbmeta-cli/internal/cache"
	"dbmeta-cli/internal/guard"
)

const cacheUsage = `用法: dbmeta-cli cache <命令>

命令:
  list [实例 [库]]            列出本地缓存:抓取时间、年龄、是否过期
  clear <实例> [库 [表]]      删除缓存`

func cacheCommand() *Command {
	return &Command{
		Name:  "cache",
		Short: "管理本地表结构缓存",
		Usage: cacheUsage,
		Sub: []*Command{
			{Name: "list", Short: "列出缓存", Usage: "用法: dbmeta-cli cache list [实例 [库]] [--dir <目录>]", Run: runCacheList},
			{Name: "clear", Short: "删除缓存", Usage: "用法: dbmeta-cli cache clear <实例> [库 [表]] [--dir <目录>]", Run: runCacheClear},
		},
	}
}

func runCacheList(_ context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("cache list", flag.ContinueOnError)
	dir := dirFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 2 {
		return usageErr("最多接受 <实例> [库] 两个参数")
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	ttl, err := p.cfg.EffectiveTTL()
	if err != nil {
		return usageErr("%v", err)
	}
	var inst, db string
	if len(pos) > 0 {
		inst = pos[0]
	}
	if len(pos) > 1 {
		db = pos[1]
	}
	entries, err := p.store(env).List(inst, db)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(env.Stdout, "(没有缓存)")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "实例\t库\t表\tfetched_at\t年龄\t状态")
	for _, e := range entries {
		if e.Unreadable {
			fmt.Fprintf(tw, "%s\t%s\t%s\t-\t-\t无法解析\n", e.Instance, e.Database, e.Table)
			continue
		}
		status := "有效"
		switch {
		case e.Stale:
			status = "stale(上次查库失败)"
		case e.Age >= ttl:
			status = "已过期"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Instance, e.Database, e.Table,
			e.FetchedAt.Format("2006-01-02 15:04"), formatDuration(e.Age), status)
	}
	return tw.Flush()
}

func runCacheClear(_ context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("cache clear", flag.ContinueOnError)
	dir := dirFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 3 {
		return usageErr("用法: dbmeta-cli cache clear <实例> [库 [表]]")
	}
	for i, kind := range []string{"", "库名", "表名"} {
		if i == 0 {
			if err := guard.InstanceName(pos[0]); err != nil {
				return err
			}
		} else if i < len(pos) {
			if err := guard.Ident(kind, pos[i]); err != nil {
				return err
			}
		}
	}
	d, err := findDir(env, *dir)
	if err != nil {
		return err
	}
	var db, table string
	if len(pos) > 1 {
		db = pos[1]
	}
	if len(pos) > 2 {
		table = pos[2]
	}
	n, err := cache.New(d).Clear(pos[0], db, table)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "已删除 %d 个表的缓存\n", n)
	return nil
}
