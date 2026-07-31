package migrations

import "embed"

// FS 是全部迁移文件，嵌入二进制。
//
// 嵌入而不是从磁盘读取：迁移在启动时自动执行，若依赖外部目录，分发出去的二进制
// 就可能在没有迁移文件的机器上跑起来，把一个空库当成已迁移的库使用。
//
//go:embed *.sql
var FS embed.FS
