package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"dbmeta-cli/internal/config"
)

func versionCommand() *Command {
	return &Command{
		Name:  "version",
		Short: "显示版本",
		Usage: "用法: dbmeta-cli version",
		Run: func(_ context.Context, env *Env, _ []string) error {
			fmt.Fprintf(env.Stdout, "dbmeta-cli %s\n", Version)
			return nil
		},
	}
}

const initUsage = `用法: dbmeta-cli init [--dir <目录>]

在当前目录创建 dbmeta/(含空的 config.json,权限 0600,以及只忽略 config.json 的 .gitignore)。
已存在的文件不会被覆盖。

选项:
  --dir <目录>   在指定项目根目录(或 dbmeta 目录)下创建`

func initCommand() *Command {
	return &Command{
		Name:  "init",
		Short: "在当前项目创建 dbmeta/ 目录",
		Usage: initUsage,
		Run: func(_ context.Context, env *Env, args []string) error {
			fs := flag.NewFlagSet("init", flag.ContinueOnError)
			dir := dirFlag(fs)
			pos, err := parseFlags(fs, args)
			if err != nil {
				return err
			}
			if err := noExtraArgs(pos); err != nil {
				return err
			}
			target, err := initTarget(env, *dir)
			if err != nil {
				return err
			}
			created, err := config.Init(target)
			if err != nil {
				return err
			}
			for _, p := range created {
				fmt.Fprintf(env.Stdout, "已创建 %s\n", displayPath(env, p))
			}
			if len(created) == 0 {
				fmt.Fprintf(env.Stdout, "%s 已初始化,无需改动\n", displayPath(env, target))
			}
			fmt.Fprintln(env.Stdout, "下一步: dbmeta-cli instance add <名称> --jdbc \"<jdbc 链接>\",然后在 config.json 中填写 password")
			fmt.Fprintln(env.Stdout, "注意: 请使用只读数据库账号。工具层的限制不能替代账号权限。")
			return nil
		},
	}
}

// initTarget is where init and the auto-init in `instance add` create dbmeta/.
func initTarget(env *Env, flagVal string) (string, error) {
	if d := explicitDir(env, flagVal); d != "" {
		return config.ResolveDir(d), nil
	}
	cwd, err := env.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, config.DirName), nil
}
