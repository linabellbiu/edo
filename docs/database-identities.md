# 数据库行号与资源标识

每张持久化表使用数据库生成的自增整数 `id`。它表示写入顺序，供列表排序和分页使用，默认按 `id DESC` 展示。

资源之间使用 UUID 关联。自增行号不进入资源关联、权限范围、任务消息、凭据加密上下文或运行快照。

| 用途 | 数据库字段 | Go 字段 |
| --- | --- | --- |
| 内部行号 | `id`，自增主键 | `RowID` |
| 资源标识 | `uuid`，唯一且非空 | `ID` |
| 资源关联 | `application_id`、`user_id` 等 UUID 字段 | `ApplicationID`、`UserID` 等 |

接口中原有的字符串 `id`、路由参数及关联字段继续表示 UUID；`row_id` 是只读行号。保留这些资源标识，避免链接、权限和已保存任务指向其他对象。GORM 关联显式指定 `references:ID`，实际引用 `uuid` 列。

关联表同样拥有自增主键，并对原来的关联字段组合保留唯一约束。Outbox 和流水线日志已有的自增 ID 继续使用，日志游标不会重排。

## 升级

停止 EDO 后备份数据库和 `.env`，使用新程序执行 `edo migrate`，完成后再启动。自增 ID 不能用来恢复真实的完成时间，也不能代替时间区间筛选。

升级保留资源标识的值，将字符串主键列改名为 `uuid`，再增加整数主键。新增行号按已有创建时间、资源标识依次生成；没有创建时间的关联表按原关联键生成。重复执行不会重排行号。

- SQLite 在同一事务中重建主键，逐表检查记录数与全部原字段，保留索引、触发器和约束。
- PostgreSQL 在事务内调整主键、约束和序列。
- MySQL 将待恢复的外键定义写入临时升级记录。DDL 中途退出后，再次执行迁移会继续恢复约束，完成后移除临时记录。
- SQLite 向 PostgreSQL 或 MySQL 迁移时，同时保留自增行号和 UUID。复制完成后恢复 PostgreSQL 各表序列，后续写入从已有最大行号之后继续。

此处沿用项目已有的 GORM 和数据库驱动，没有增加依赖。SQLite 修改主键需要重建表；GORM 未导出该重建过程，升级代码只拆分并保留本次需要调整的建表定义。维护时需特别验证包含引号、默认值、组合唯一约束和外键的升级，以及三种数据库的重复执行和继续写入。

实现依据：[GORM 模型](https://gorm.io/docs/models.html)、[SQLite 表结构调整](https://www.sqlite.org/lang_altertable.html)、[PostgreSQL Identity Columns](https://www.postgresql.org/docs/current/ddl-identity-columns.html)。
