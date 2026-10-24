---
instance: order_prod
database: order
table: t_order
server: 10.0.0.12:3306
server_version: 8.0.32
engine: InnoDB
charset: utf8mb4
rows_estimate: 12300000
comment: "订单主表"
fetched_at: 2026-10-24T17:50:00+08:00
schema_hash: 8a93a81f037b46a25c2af7dd7b8c0ba370072800b496e2bde5f1a3a730485ef8
stale: false
---
# order_prod / order / t_order

> 订单主表  | 引擎: InnoDB | 字符集: utf8mb4 | 估算行数: ~12,300,000(估算值)

## 字段
| # | 字段 | 类型 | 可空 | 默认值 | 键 | 额外 | 注释 |
|---|---|---|---|---|---|---|---|
| 1 | id | bigint unsigned | NO | NULL | PRI | auto_increment | 主键 |
| 2 | status | tinyint | NO | 0 | MUL |  | 0=待付款\|1=已付款 |
| 3 | remark | varchar(255) | YES | '' |  |  | 备注\n第二行 |
| 4 | created_at | datetime | NO | CURRENT_TIMESTAMP |  | DEFAULT_GENERATED |  |

## 索引
| 名称 | 唯一 | 字段(按顺序) | 类型 | 注释 |
|---|---|---|---|---|
| PRIMARY | YES | id | BTREE |  |
| idx_status_created | NO | status, created_at | BTREE | 状态+时间 |

## DDL
```sql
CREATE TABLE `t_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=100 DEFAULT CHARSET=utf8mb4
```
