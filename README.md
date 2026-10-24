# dbmeta-cli

只读的数据库表结构查询工具,给 AI agent 和人用。只做一件事:取**单个表**的结构(字段、索引、DDL),并在本地缓存成 Markdown。

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
cd <你的项目>
dbmeta-cli init
dbmeta-cli instance add order_prod --env prod --desc "订单主库" \
  --jdbc "jdbc:mysql://10.0.0.12:3306/order?user=readonly&useSSL=false"
# 手动编辑 dbmeta/config.json,填写 password
dbmeta-cli instance ping order_prod
dbmeta-cli table get order_prod order t_order
```

## 配置

项目里的 `dbmeta/config.json` 是唯一配置文件(连接信息和工具设置合一),含密码,权限 0600,`init` 会把它写进 `dbmeta/.gitignore`。权限比 0600 宽时拒绝读取。

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

**配置发现:** 从当前目录向上找 `dbmeta/config.json`,也可以用 `--dir` 或环境变量 `DBMETA_DIR` 指定(项目根目录或 `dbmeta` 目录都行)。

**缓存:** `dbmeta/<实例>/<库>/<表>.md`,可直接浏览。缓存只含表结构,团队可以自行决定是否提交;配置不会随 git 共享,每人用 `instance add --jdbc` 各生成一份。

## 命令

| 命令 | 作用 | 连库 |
|---|---|---|
| `init` | 创建 `dbmeta/`、空 `config.json`、`.gitignore` | 否 |
| `instance add <名称> --jdbc <url>` | 解析 JDBC 链接并登记(密码一律忽略) | 否 |
| `instance add <名称> --host <h> [--port --user --tls]` | 手动登记 | 否 |
| `instance list [--json]` | 列出实例,不显示密码 | 否 |
| `instance ping <实例>` | 连接并执行 `SELECT 1` | 是 |
| `table get <实例> <库> <表>` | 取表结构 | 视缓存 |
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

### `table get`

```bash
dbmeta-cli table get <实例> <库> <表> [--refresh | --cached] [--ttl 24h] [--format md|json] [-o 文件] [--print-path]
```

| 模式 | 行为 |
|---|---|
| 默认 | 缓存在有效期内直接读(不联网、不需密码);缺失或过期则查库并回写。查库失败且有旧缓存时返回旧缓存,stderr 警告,文件头标记 `stale: true`。表已不存在(退出码 4)时不会回退。 |
| `--refresh` | 一律查库;失败就报错,不回退到旧缓存 |
| `--cached` | 绝不联网;缺失返回 5;已过期仍返回内容并警告 |

- stdout 只输出表结构,来源写在 stderr,例如 `dbmeta: cache hit (fetched 2026-10-21, age 3d, ttl 7d)`。
- 缓存的新鲜度看文件头里的 `fetched_at`,不看文件 mtime。
- 刷新时对比 `schema_hash`(字段、索引、DDL;忽略 `AUTO_INCREMENT=N` 和行数估算)。变化时 stderr 提示"表结构已变化",并在文件头记录 `changed_at`。
- `--print-path` 只输出缓存文件路径,列很多的表可以让 agent 用 Read 工具去读。

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 其他错误 |
| 2 | 参数、标识符、实例或配置错误(包括未找到 `dbmeta/`) |
| 3 | 密码为空或认证失败 |
| 4 | 表不存在或无权限查看 |
| 5 | `--cached` 缓存未命中 |
| 6 | 网络或连接失败 |
| 7 | 不在 `allow_databases` 内 |

## 安全设计

1. **没有入口。** 没有 `sql`、`exec` 命令,也没有列库、列表、扫描能力。数据库层只有 `FetchTable(db, table)`。
2. **标识符校验。** 库名和表名必须匹配 `^[\p{L}\p{N}_$-]{1,64}$`。通配符、反引号、引号、点、空格、路径分隔符、`..` 都被拒绝(退出码 2),缓存路径因此不会被穿越。
3. **SQL 白名单。** 所有 SQL 是编译期常量:`information_schema.TABLES` / `COLUMNS` / `STATISTICS`(参数化,按 `TABLE_SCHEMA` 和 `TABLE_NAME` 过滤)、`SHOW CREATE TABLE`(标识符已校验)。另有 `SELECT VERSION()`、`SELECT 1`(ping)和会话设置语句。不会执行 `SHOW DATABASES`、`SHOW TABLES`,也不会 `SELECT` 业务表。
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

项目根是 `dbmeta/` 的上一级,没有 `dbmeta/` 时取当前目录。`--dir` 优先于 `--target` 和 `--scope`。

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

代码结构:`internal/config`(目录发现、配置读写)、`jdbc`(链接解析)、`guard`(标识符和白名单)、`source`(接口)、`source/mysql`(实现)、`schema`(模型、hash、Markdown)、`cache`(存取和决策)、`cli`(命令)。
