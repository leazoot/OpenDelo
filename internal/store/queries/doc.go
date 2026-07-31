// Package queries 存放 sqlc 的 SQL 输入与由其生成的类型安全查询代码。
//
// 禁止字符串拼接 SQL 与 SELECT *；动态排序字段走白名单映射。
//
// # SQL 文件必须是纯 ASCII
//
// sqlc v1.27 的 SQLite 引擎按字节偏移切分查询，却按 rune 剥离注释。`.sql` 里出现
// 任何非 ASCII 字符（例如中文注释），后续每个查询的 SQL 常量都会被错位截断 ——
// 生成的代码照样能编译、能通过 lint，只在运行时报「no such column」这类语法错误。
// 因此说明性文字一律写在本文件里，`.sql` 只放 SQL 与 sqlc 指令。
// 该约束由 TestQuerySources_AreASCIIOnly 强制。
//
// # settings 的查询约定
//
// UpdateSettingValue 只改 value 与 updated_at：改一次取值不应让偏好的创建时间丢失，
// 因此 id 与 created_at 不进 SET 子句。更新不存在的名字返回 sql.ErrNoRows，不会
// 悄悄变成一次插入。
//
// DeleteSetting 对不存在的名字不报错 —— 收回一个偏好是幂等操作。
package queries
