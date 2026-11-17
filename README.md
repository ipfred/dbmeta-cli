# dbmeta-cli

只读的数据库表结构查询工具,给 AI agent 和人用。只做一件事:取表的结构(字段、索引、DDL),并在本地缓存成 Markdown。只需要写 `库.表`,工具按 `.dbmeta/routes.json` 里的 库 → 实例 映射自己找到实例。

第一版支持 MySQL 协议(MySQL、MariaDB、TiDB 等)。

> **请始终使用只读数据库账号。** 工具层的限制不能替代账号权限。

## 安装

```bash
make install          # 二进制装到 ~/.local/bin,并安装全局 skill 到 ~/.agents/skills/dbmeta
# 或
go install ./cmd/dbmeta-cli
make dist             # 交叉编译 darwin / linux / windows
```

## 快速开始

```bash
dbmeta-cli init --scope global       # 所有项目共用(装在用户目录下);只想用于当前项目就 cd 进去后运行 dbmeta-cli init
dbmeta-cli instance add order_prod --env prod --desc "订单主库" \
  --jdbc "jdbc:mysql://10.0.0.12:3306/order?user=readonly&useSSL=false"
# 手动编辑 config.json(位置见下面"配置发现"),填写 password
dbmeta-cli instance ping order_prod
dbmeta-cli db sync order_prod        # 登记这个实例里有哪些库(写入 .dbmeta/routes.json)
dbmeta-cli table get order.t_order   # 之后只写 库.表,不用再指定实例
```

实例有 20 多个时,全部 `instance add` 并填好密码后运行一次 `dbmeta-cli db sync` 即可。以后新增实例,只需要对它运行 `db sync <实例>`;漏掉也没关系,`table get` 在 routes.json 里找不到库时会自动同步。

## 配置

项目里的 `.dbmeta/config.json` 是唯一配置文件(连接信息和工具设置合一),含密码,权限 0600,`init` 会把它写进 `.dbmeta/.gitignore`。权限比 0600 宽时拒绝读取。

```json
{
  "ttl": "168h",
  "instances": {
    "order_prod": {
      "host": "10.0.0.12",
      "port": 3306,
      "user": "readonly",
      "password": "",
      "tls": "preferred",
      "env": "prod",
      "desc": "订单主库(只读账号)",
      "allow_databases": ["order", "order_history"]
    }
  }
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `ttl` | 否 | 缓存有效期,默认 `168h`(7 天),也接受 `7d` |
| `host` / `port` | 是 | port 缺省 3306 |
| `user` / `password` | 是 | 密码为空表示未配置,`table get` 在连库前返回退出码 3 |
| `tls` | 否 | `false`、`preferred`、`true`、`skip-verify`,默认 `preferred` |
| `env` / `desc` | 否 | 展示用 |
| `allow_databases` | 否 | 库白名单,不写则不限制 |

字段拼错会直接报错(不接受未知字段)。

**配置发现:** 按下面的顺序,用第一个找到的 `.dbmeta/`:

1. `--dir` 或环境变量 `DBMETA_DIR`(项目根目录或 `.dbmeta` 目录都行)。指定了就只看它,找不到不会再往下回退。
2. 从当前目录向上找到的第一个 `.dbmeta/config.json`(项目级,类似 git 找 `.git`)。
3. 用户目录下的 `.dbmeta/`(全局):

   | 系统 | 位置 |
   |---|---|
   | Windows | `%USERPROFILE%\.dbmeta`,例如 `C:\Users\pa\.dbmeta` |
   | macOS | `~/.dbmeta`,例如 `/Users/pa/.dbmeta` |
   | Linux | `~/.dbmeta` |

   用 `dbmeta-cli init --scope global` 创建。所有自己没有 `.dbmeta/` 的项目共用这一份:实例只登记一次,`routes.json` 和表结构缓存也只有一份。项目里有自己的 `.dbmeta/` 时,项目的完全接管,不和全局合并。

`init` 的位置由 `--scope project|global` 选择,默认 `project`(当前目录)。`instance add` 在找不到任何 `.dbmeta/` 时会在当前目录自动创建项目级的;想用全局的,先运行 `init --scope global`。全局目录里同样含密码:Linux 和 macOS 上 `config.json` 权限为 0600,权限过宽时拒绝读取;Windows 上请确认用户目录只有自己能访问。`skill install --scope project` 不会把全局 `.dbmeta/` 当作项目根。

**缓存:** `.dbmeta/<实例>/<库>/<表>.md`,可直接浏览。缓存只含表结构,团队可以自行决定是否提交;配置不会随 git 共享,每人用 `instance add --jdbc` 各生成一份。实例里确认没有某张表时,会写一个 `<表>.miss` 标记(有效期同 `ttl`),避免每次都重连没有这张表的实例;`--refresh` 会绕过它。

**路由(库 → 实例):** `.dbmeta/routes.json`,由 `db sync` 生成,不含密码,默认不被 `.gitignore` 忽略,可以提交 git 共享给团队。

```json
{
  "version": 1,
  "instances": { "order_prod": { "synced_at": "2026-11-17T10:30:00+08:00", "attempted_at": "2026-11-17T10:30:00+08:00" } },
  "databases": { "order": ["order_prod"], "temp_x": ["test", "order_prod"] }
}
```

一个库对应多个实例(测试/生产、主/备)是正常情况,值是实例数组。文件内容可以直接阅读,不建议手工修改,下次 `db sync` 会按实例整体覆盖。

## 命令

| 命令 | 作用 | 连库 |
|---|---|---|
| `init [--scope project\|global]` | 创建 `.dbmeta/`、空 `config.json`、`.gitignore`;`global` 建在用户目录下 | 否 |
| `instance add <名称> --jdbc <url>` | 解析 JDBC 链接并登记(密码一律忽略) | 否 |
| `instance add <名称> --host <h> [--port --user --tls]` | 手动登记 | 否 |
| `instance list [--json]` | 列出实例,不显示密码 | 否 |
| `instance ping <实例>` | 连接并执行 `SELECT 1` | 是 |
| `db sync [实例]` | 读取实例里的库名,写入 routes.json(不写实例 = 全部) | 是 |
| `db list [--db 库] [--json]` | 查看 库 → 实例 映射 | 否 |
| `table get <库.表>... [-i 实例]` | 取表结构,可一次多个 | 视缓存 |
| `cache list [实例 [库]]` | 列出缓存及是否过期 | 否 |
| `cache clear <实例> [库 [表]]` | 删除缓存 | 否 |
| `skill install / uninstall / print` | 管理 AI agent 的 skill | 否 |
| `version` | | 否 |

`instance add` 的选项:`--env`、`--desc`、`--allow-db a,b`、`--force`。`--force` 覆盖同名实例时,只有主机、端口、用户都没变才保留原密码。

### JDBC 链接

支持 `jdbc:mysql://` 和 `jdbc:mariadb://`,包括 userinfo 形式、`loadbalance` / `replication`、多主机(取第一个)、`address=(host=..)(port=..)` 形式。

- 密码(`password=` 或 userinfo)一律忽略,`password` 写空串。
- `sslMode` / `useSSL` 映射到 `tls`:`DISABLED` 或 `useSSL=false` → `false`;`PREFERRED` → `preferred`;`REQUIRED`、`VERIFY_*`、`useSSL=true` → `true`。
- URL 里的默认库不会写入 `allow_databases`,需要限制时显式传 `--allow-db`。
- 其他参数会被忽略,并在输出里列出参数名。

### `db sync` / `db list`

```bash
dbmeta-cli db sync              # 全部实例(最多 4 个并发),并清理 config.json 里已不存在的实例
dbmeta-cli db sync order_prod   # 只同步一个实例
dbmeta-cli db list [--db order] [--json]
```

- `db sync` 对每个实例只执行一条固定查询 `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA`,账号看不到的库不会出现。系统库(`information_schema`、`mysql`、`performance_schema`、`sys`)、名称不合法的库和不在该实例 `allow_databases` 里的库会被排除。
- 按实例整体替换:库被删除后,同步一次记录就会消失。某个实例失败(没填密码、连不上)时保留它原来的记录,其余实例照常更新;全部失败才返回错误(退出码按 3 认证 > 6 网络 > 其他)。
- `db list` 只读本地文件,不联网。

### `table get`

```bash
dbmeta-cli table get <库.表>... [-i <实例>] [--refresh | --cached] [--ttl 24h] [--format md|json] [-o 文件] [--print-path]
```

参数只接受 `库.表`(旧的 `<实例> <库> <表>` 写法已移除,会提示新写法)。可以一次写多个,同一个实例只连一次。

**不写 `-i`:** 按 routes.json 找到有这个库的**所有**实例,并发(最多 4 个)逐个取:

- 哪个实例有这张表就输出哪个;没有这张表或连不上的实例只在 stderr 写一行 `已跳过`,不算失败。
- 结果多于一个时,每份前面有一行 `<!-- 库.表 @ 实例 -->`;结构(`schema_hash`)相同的只完整输出一份,其余写"结构与 xxx 相同"。只有一个结果时,输出和缓存文件完全一致。
- 一张表在所有实例里都取不到,才算失败。批量里有的表失败时,其余照常输出,退出码取最严重的一个(3 > 6 > 4 > 5)。
- routes.json 里没有这个库时,先自动同步"从没试过、或超过 1 小时没试过"的实例(有密码的),再重查;仍然没有,返回退出码 2。失败的同步也会记录,1 小时内不会重复尝试。`--cached` 永远不联网,也就不会自动同步。

**写 `-i <实例>`:** 只查这个实例,不依赖 routes.json,仍受 `allow_databases` 限制。

| 模式 | 行为 |
|---|---|
| 默认 | 缓存在有效期内直接读(不联网、不需密码);缺失或过期则查库并回写。查库失败且有旧缓存时返回旧缓存,stderr 警告,文件头标记 `stale: true`。表已不存在(退出码 4)时不会回退。 |
| `--refresh` | 一律查库;失败就报错,不回退到旧缓存 |
| `--cached` | 绝不联网;缺失返回 5;已过期仍返回内容并警告 |

- stdout 只输出表结构,来源写在 stderr,例如 `dbmeta: cache hit (fetched 2026-10-21, age 3d, ttl 7d)`。
- 缓存的新鲜度看文件头里的 `fetched_at`,不看文件 mtime。
- 刷新时对比 `schema_hash`(字段、索引、DDL;忽略 `AUTO_INCREMENT=N` 和行数估算)。变化时 stderr 提示"表结构已变化",并在文件头记录 `changed_at`。
- `--print-path` 只输出缓存文件路径(每个结果一行),列很多的表可以让 agent 用 Read 工具去读。
- `--format json`:只有一个结果时是对象,多个结果时是数组(每个元素带 `instance` 字段)。

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 其他错误 |
| 2 | 参数、标识符、库、实例或配置错误(包括未找到 `.dbmeta/`、库不在任何实例里) |
| 3 | 密码为空或认证失败 |
| 4 | 表不存在或无权限查看 |
| 5 | `--cached` 缓存未命中 |
| 6 | 网络或连接失败 |
| 7 | 不在 `allow_databases` 内 |

## 安全设计

1. **没有入口。** 没有 `sql`、`exec` 命令,也没有列表、扫描能力。数据库层只有两个方法:`FetchTable(db, table)` 和 `ListDatabases()`(只返回账号可见的库名,用来建立 库 → 实例 映射)。
2. **标识符校验。** 库名和表名必须匹配 `^[\p{L}\p{N}_$-]{1,64}$`。通配符、反引号、引号、点、空格、路径分隔符、`..` 都被拒绝(退出码 2),缓存路径因此不会被穿越。
3. **SQL 白名单。** 所有 SQL 是编译期常量:`information_schema.TABLES` / `COLUMNS` / `STATISTICS`(参数化,按 `TABLE_SCHEMA` 和 `TABLE_NAME` 过滤)、`SHOW CREATE TABLE`(标识符已校验)。另有一条无参数的 `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA`(只供 `db sync` 使用,只取库名)、`SELECT VERSION()`、`SELECT 1`(ping)和会话设置语句。不会执行 `SHOW DATABASES`、`SHOW TABLES`,也不会 `SELECT` 业务表。
4. **不做模糊建议。** 表不存在只返回"未找到",不会列出相似的表名。
5. **连接加固。** 单连接,`SET SESSION TRANSACTION READ ONLY`,设置语句超时和连接、读写超时。个别服务器(如 TiDB)不支持的设置只会警告,不会失败。
6. **白名单先于连接。** `allow_databases` 不通过时不会建立连接,缓存读取也一样受限。
7. **不采集数据。** 只拿结构和估算行数。
8. **密码从不上命令行。** 没有 `--password` 选项,JDBC 里的密码也会被丢弃。

测试里用 `go-sqlmock` 的严格匹配:出现任何白名单之外的 SQL 就失败;另有一个测试扫描源码,确保每个查询调用点都用的是白名单常量。

## Skill

skill 内嵌在二进制里,只含说明文件,不含任何连接信息。

```bash
dbmeta-cli skill install [--scope global|project] [--target agents,claude,factory,codex,opencode,all] [--dir <目录>]
dbmeta-cli skill uninstall   # 同样的参数
dbmeta-cli skill print
```

| scope | target=agents 时的位置 |
|---|---|
| `global`(默认) | `~/.agents/skills/dbmeta/SKILL.md` |
| `project` | `<项目根>/.agents/skills/dbmeta/SKILL.md` |

项目根是 `.dbmeta/` 的上一级,没有 `.dbmeta/` 时取当前目录。`--dir` 优先于 `--target` 和 `--scope`。

## 多平台构建与发布(GitHub Actions)

workflow 文件:`.github/workflows/build.yml`。流程:`go vet` + `go test` → 交叉编译 → 打包 → (可选)发布 Release。

产物(`<版本>` 不带 `v`):

| 平台 | 文件 |
|---|---|
| macOS Apple Silicon / Intel | `dbmeta-cli_<版本>_darwin_arm64.tar.gz` / `..._darwin_amd64.tar.gz` |
| Linux x86_64 / ARM64 | `dbmeta-cli_<版本>_linux_amd64.tar.gz` / `..._linux_arm64.tar.gz` |
| Windows x86_64 | `dbmeta-cli_<版本>_windows_amd64.zip` |
| 校验和(仅 Release) | `SHA256SUMS` |

### 触发方式

**方式一:推送 tag(推荐,自动发布 Release)**

```bash
git tag v0.2.0
git push origin v0.2.0
```

构建完成后,在仓库 Releases 页面下载全部平台的包。

**方式二:手动触发,只构建不发布**

GitHub 页面:Actions → build → Run workflow(版本号可留空,默认 `dev-<短 commit>`),完成后在该次运行页面底部的 Artifacts 下载。

用 [GitHub CLI](https://cli.github.com/):

```bash
gh workflow run build.yml                                  # 只构建
gh workflow run build.yml -f version=0.2.0 -f release=true # 构建并发布 v0.2.0
gh run list --workflow build.yml                           # 查看运行状态
gh run watch                                               # 实时跟踪
gh run download <run-id>                                   # 下载 Artifacts
```

注意:

- 手动触发时,workflow 必须已经在默认分支上。
- 发布 Release 必须填写版本号;tag 不存在时会自动在触发时的 commit 上创建。
- 版本号通过 `-ldflags -X` 写入二进制,`dbmeta-cli version` 可以确认。
- 需要仓库允许 Actions 写权限:Settings → Actions → General → Workflow permissions 选 "Read and write permissions"(只影响发布 Release)。

### 安装下载的包

```bash
tar -xzf dbmeta-cli_0.2.0_darwin_arm64.tar.gz
install -m 0755 dbmeta-cli_0.2.0_darwin_arm64/dbmeta-cli ~/.local/bin/
dbmeta-cli skill install
```

macOS 从浏览器下载的二进制可能被 Gatekeeper 拦截,执行 `xattr -d com.apple.quarantine <文件>` 即可。

## 开发

```bash
make test                    # go vet + go test
DBMETA_TEST_DSN='readonly:secret@tcp(127.0.0.1:3306)/' \
  go test ./internal/source/mysql -run Integration -v   # 可选的真实库集成测试,默认跳过
```

代码结构:`internal/config`(目录发现、配置读写)、`jdbc`(链接解析)、`routes`(库 → 实例 映射)、`guard`(标识符和白名单)、`source`(接口)、`source/mysql`(实现)、`schema`(模型、hash、Markdown)、`cache`(存取和决策)、`cli`(命令)。
