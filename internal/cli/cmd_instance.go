package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"dbmeta-cli/internal/config"
	"dbmeta-cli/internal/guard"
	"dbmeta-cli/internal/jdbc"
	"dbmeta-cli/internal/source"
)

const instanceUsage = `用法: dbmeta-cli instance <命令>

命令:
  add <名称> --jdbc <url>    解析 JDBC 链接并登记实例(密码一律忽略,需手动填写)
  add <名称> --host <主机>   手动登记实例
  list [--json]              列出实例(不显示密码)
  ping <实例>                连接并执行 SELECT 1,验证账号可用`

func instanceCommand() *Command {
	return &Command{
		Name:  "instance",
		Short: "管理数据库实例",
		Usage: instanceUsage,
		Sub: []*Command{
			{Name: "add", Short: "登记实例", Usage: addUsage, Run: runInstanceAdd},
			{Name: "list", Short: "列出实例", Usage: "用法: dbmeta-cli instance list [--json] [--dir <目录>]", Run: runInstanceList},
			{Name: "ping", Short: "验证实例账号可用", Usage: "用法: dbmeta-cli instance ping <实例> [--dir <目录>]", Run: runInstancePing},
		},
	}
}

const addUsage = `用法: dbmeta-cli instance add <名称> --jdbc <url> [选项]
       dbmeta-cli instance add <名称> --host <主机> [--port <端口>] [--user <用户>] [选项]

选项:
  --jdbc <url>       JDBC 链接,支持 jdbc:mysql: 和 jdbc:mariadb:。链接中的密码会被忽略。
  --host <主机>      手动指定主机(与 --jdbc 二选一)
  --port <端口>      默认 3306
  --user <用户>      覆盖或补充链接中的用户
  --tls <模式>       false | preferred | true | skip-verify
  --env <环境>       例如 prod、test,帮助选实例
  --desc <说明>      实例说明
  --allow-db <a,b>   库白名单,逗号分隔;不写则不限制
  --force            同名实例已存在时覆盖
  --dir <目录>       ` + dirFlagHelp + `

dbmeta/ 不存在时会先自动创建。`

func runInstanceAdd(_ context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("instance add", flag.ContinueOnError)
	dir := dirFlag(fs)
	jdbcURL := fs.String("jdbc", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	user := fs.String("user", "", "")
	tls := fs.String("tls", "", "")
	envName := fs.String("env", "", "")
	desc := fs.String("desc", "", "")
	allowDB := fs.String("allow-db", "", "")
	force := fs.Bool("force", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("需要一个实例名称\n\n%s", addUsage)
	}
	name := pos[0]
	if err := guard.InstanceName(name); err != nil {
		return err
	}
	if (*jdbcURL == "") == (*host == "") {
		return usageErr("--jdbc 和 --host 必须二选一")
	}

	inst := &config.Instance{
		Port: config.DefaultPort, User: *user, TLS: *tls, Env: *envName, Desc: *desc,
	}
	var res *jdbc.Result
	if *jdbcURL != "" {
		if res, err = jdbc.Parse(*jdbcURL); err != nil {
			return usageErr("%v", err)
		}
		inst.Host, inst.Port = res.Host, res.Port
		if inst.User == "" {
			inst.User = res.User
		}
		if inst.TLS == "" {
			inst.TLS = res.TLS
		}
	} else {
		inst.Host = strings.TrimSpace(*host)
	}
	if *port != 0 {
		inst.Port = *port
	}
	for _, db := range strings.Split(*allowDB, ",") {
		if db = strings.TrimSpace(db); db != "" {
			inst.AllowDatabases = append(inst.AllowDatabases, db)
		}
	}
	if err := inst.Validate(); err != nil {
		return usageErr("%v", err)
	}

	dbmetaDir, autoInit, err := ensureProjectDir(env, *dir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(dbmetaDir)
	if err != nil {
		return &CodedError{Code: ExitUsage, Err: err}
	}
	keptPassword := false
	if existing, ok := cfg.Instances[name]; ok {
		if !*force {
			return usageErr("实例 %q 已存在,如需覆盖请加 --force", name)
		}
		// Keep the password only when the target is unchanged, so a typo'd
		// --force can never send an old credential to a different server.
		if existing.Host == inst.Host && existing.EffectivePort() == inst.EffectivePort() && existing.User == inst.User {
			inst.Password, keptPassword = existing.Password, existing.Password != ""
		}
	}
	cfg.Instances[name] = inst
	if err := config.Save(dbmetaDir, cfg); err != nil {
		return err
	}

	out := env.Stdout
	cfgPath := displayPath(env, config.ConfigPath(dbmetaDir))
	if autoInit {
		fmt.Fprintf(out, "已初始化 %s\n", displayPath(env, dbmetaDir))
	}
	fmt.Fprintf(out, "已写入 %s  instances.%s  %s  user=%s  tls=%s\n", cfgPath, name, inst.Addr(), orDash(inst.User), tlsLabel(inst))
	switch {
	case keptPassword:
		fmt.Fprintln(out, "password 沿用原有配置(主机和用户未变)")
	default:
		fmt.Fprintf(out, "password 为空,请手动编辑 %s 填写\n", cfgPath)
	}
	if inst.User == "" {
		fmt.Fprintf(out, "user 为空,请手动编辑 %s 填写\n", cfgPath)
	}
	if res != nil {
		for _, n := range res.Notes {
			fmt.Fprintf(out, "提示: %s\n", n)
		}
		if res.Database != "" {
			fmt.Fprintf(out, "提示: URL 中的默认库 %s 未写入白名单;如需限制请用 --allow-db\n", res.Database)
		}
		if len(res.Ignored) > 0 {
			fmt.Fprintf(out, "已忽略的参数: %s\n", strings.Join(res.Ignored, ", "))
		}
	}
	return nil
}

// ensureProjectDir finds dbmeta/ or creates it, reporting whether it created one.
func ensureProjectDir(env *Env, flagVal string) (dir string, created bool, err error) {
	dir, err = findDir(env, flagVal)
	if err == nil {
		return dir, false, nil
	}
	if !errors.Is(err, config.ErrNotFound) {
		return "", false, err
	}
	if dir, err = initTarget(env, flagVal); err != nil {
		return "", false, err
	}
	if _, err = config.Init(dir); err != nil {
		return "", false, err
	}
	return dir, true, nil
}

func tlsLabel(i *config.Instance) string {
	if i.TLS == "" {
		return config.DefaultTLS + "(默认)"
	}
	return i.TLS
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type instanceRow struct {
	Name            string   `json:"name"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	User            string   `json:"user"`
	TLS             string   `json:"tls"`
	Env             string   `json:"env"`
	Desc            string   `json:"desc"`
	AllowDatabases  []string `json:"allow_databases"`
	PasswordSet     bool     `json:"password_set"`
	CachedDatabases int      `json:"cached_databases"`
	CachedTables    int      `json:"cached_tables"`
}

func runInstanceList(_ context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("instance list", flag.ContinueOnError)
	dir := dirFlag(fs)
	asJSON := fs.Bool("json", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if err := noExtraArgs(pos); err != nil {
		return err
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	store := p.store(env)

	rows := []instanceRow{}
	for _, name := range p.cfg.Names() {
		i := p.cfg.Instances[name]
		dbs, tabs := store.Count(name)
		allow := i.AllowDatabases
		if allow == nil {
			allow = []string{}
		}
		rows = append(rows, instanceRow{
			Name: name, Host: i.Host, Port: i.EffectivePort(), User: i.User, TLS: i.EffectiveTLS(),
			Env: i.Env, Desc: i.Desc, AllowDatabases: allow, PasswordSet: i.Password != "",
			CachedDatabases: dbs, CachedTables: tabs,
		})
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
		fmt.Fprintln(env.Stdout, "(还没有实例,用 dbmeta-cli instance add <名称> --jdbc \"<url>\" 添加)")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "名称\t地址\t用户\tENV\t说明\t库白名单\t密码\t本地缓存")
	for _, r := range rows {
		allow := "不限"
		if len(r.AllowDatabases) > 0 {
			allow = strings.Join(r.AllowDatabases, ",")
		}
		pw := "未填写"
		if r.PasswordSet {
			pw = "已填写"
		}
		fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%s\t%s\t%s\t%d 库 / %d 表\n",
			r.Name, r.Host, r.Port, orDash(r.User), orDash(r.Env), orDash(r.Desc), allow, pw, r.CachedDatabases, r.CachedTables)
	}
	return tw.Flush()
}

// requireCredentials fails before any network traffic when the instance cannot log in.
func requireCredentials(p *project, name string, i *config.Instance) error {
	if i.User == "" {
		return usageErr("实例 %q 的 user 为空,请编辑 %s 填写", name, config.ConfigPath(p.dir))
	}
	if i.Password == "" {
		return fmt.Errorf("%w: 实例 %q 的 password 为空,请用户自行编辑 %s 填写", source.ErrAuth, name, config.ConfigPath(p.dir))
	}
	return nil
}

func runInstancePing(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("instance ping", flag.ContinueOnError)
	dir := dirFlag(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("需要一个实例名称")
	}
	p, err := loadProject(env, *dir)
	if err != nil {
		return err
	}
	inst, err := p.cfg.Instance(pos[0])
	if err != nil {
		return usageErr("%v", err)
	}
	if err := requireCredentials(p, pos[0], inst); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	src, err := env.Open(ctx, inst, env.warn)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Ping(ctx); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "OK  %s  %s  user=%s\n", pos[0], inst.Addr(), inst.User)
	return nil
}

func (e *Env) warn(format string, a ...any) { e.logf("警告: "+format, a...) }
