// Package migrations 存放 goose 迁移文件并将其嵌入二进制。
//
// 迁移文件一旦提交即不可修改，需要修正时新增一个迁移。每个迁移的 Down 必须真正可回滚。
package migrations
