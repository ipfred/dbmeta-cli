---
name: dbmeta
description: 查询数据库表的结构(字段、类型、注释、索引、DDL)。当用户想了解某个表的结构,或写 SQL、ORM 模型、迁移脚本、代码时需要补充表结构上下文,或要把 JDBC 配置登记为 dbmeta 实例时使用。通过本机的 dbmeta-cli 只读获取,带本地缓存,不会执行任意 SQL。
---

# 查询数据库表结构

用本机的 `dbmeta-cli` 获取单个表的结构。连接信息在当前项目的 `dbmeta/config.json`,只有 dbmeta-cli 会读它。

## 1. 选实例

```bash
dbmeta-cli instance list
```

按 `ENV` 和说明选实例。拿不准选哪个(例如同时有 test 和 prod)时,先问用户。

## 2. 获取表结构

```bash
dbmeta-cli table get <实例> <库> <表>
```

- 表名来自用户,或来自项目代码里的 ORM 模型、SQL。不要猜表名,也不要遍历。
- stdout 只有表结构(Markdown),来源和警告在 stderr。
- 表很宽时,用 `--print-path` 拿到缓存文件路径,再用 Read 工具读文件:

```bash
dbmeta-cli table get <实例> <库> <表> --print-path
```

- 需要结构化数据时加 `--format json`。

## 3. 缓存规则

| 场景 | 参数 |
|---|---|
| 日常理解表结构 | 默认。缓存在有效期内(默认 7 天)直接读缓存,过期才查库 |
| 用户说"刚改了表"、"迁移后",或 SQL 报 `Unknown column`、`doesn't exist` | `--refresh` 强制查库 |
| 没有密码、离线、或查库失败 | `--cached` 只读缓存,不联网 |

stderr 里出现 `stale` 警告,说明返回的是过期缓存,要告诉用户数据可能不是最新的。出现"表结构已变化"时,提醒用户表刚被改过。

## 4. 登记新实例

```bash
dbmeta-cli instance add <名称> --jdbc "<jdbc 链接>" --env <prod|test|...> --desc "<说明>"
```

- 如果 JDBC 链接里带了密码,先把密码去掉再传入,也不要在对话里复述密码。
- 完成后告诉用户:去 `dbmeta/config.json` 里自己填写 `password`。
- `dbmeta/` 不存在时会自动创建。也可以让用户先运行 `dbmeta-cli init`。
- 如需限制可访问的库,加 `--allow-db a,b`。

## 5. 失败时怎么办

| 退出码 | 含义 | 处理 |
|---|---|---|
| 2 | 参数、标识符、实例或配置错误 | 若提示"未找到 dbmeta/config.json",让用户运行 `dbmeta-cli init`;否则按提示修正 |
| 3 | 密码为空或认证失败 | 让用户自己编辑 `dbmeta/config.json` 填写密码。不要向用户索要密码 |
| 4 | 表不存在,或账号无权限查看 | 请用户确认库名、表名和权限。不要重试,不要猜相似的表名 |
| 5 | `--cached` 没有缓存 | 去掉 `--cached` 再试,或让用户先配置好密码 |
| 6 | 网络或连接失败 | 改用 `--cached`,并告知用户数据可能已过期 |
| 7 | 库不在 `allow_databases` 白名单内 | 不要重试。请用户确认是否需要放开白名单 |
| 127 / command not found | 未安装 dbmeta-cli | 告诉用户先安装 dbmeta-cli |

## 6. 禁止事项

- 不要用 mysql 客户端、脚本或任何其他方式直连数据库,只能用 `dbmeta-cli`。
- 不要向用户索要数据库密码。
- 不要读取、打印、复述 `dbmeta/config.json`,里面有密码。查看实例用 `instance list`。
- 不要批量遍历表,只取当前任务需要的表。
