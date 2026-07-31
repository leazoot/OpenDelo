// Package store 是 SQLite 访问层，也是本产品唯一的持久化入口。
//
// 只做数据访问，不做业务判断。只有本包及其子包可以 import 数据库驱动。
package store
