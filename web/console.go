// Package web 只做一件事：把 Vite 产物内嵌进二进制，让 opendelo 是单个可执行文件。
//
// 这个包放在 web/ 而不是 internal/ 下，是因为 go:embed 只能引用包目录内的文件。
// 换个位置就得在构建时把 dist 复制一份，而两份产物随时可能不同步 ——
// 直接编译（不经 make）时嵌进去的就会是上一次的界面。
package web

import (
	"embed"
	"io/fs"
)

// distDir 是 Vite 的输出目录，同时也是 fs.Sub 要剥掉的前缀。
//
// 它嵌在 embedded/ 之下而不是直接叫 dist，是因为 Vite 每次构建都会清空 outDir：
// 占位文件必须放在 Vite 管不着的地方，否则构建一次就没了。
const distDir = "embedded/dist"

// all: 前缀让 embedded/.gitkeep 这个点文件也被匹配。没有它，尚未构建前端的工作区里
// embedded/ 是空目录，go:embed 会因「contains no embeddable files」让整个仓库编译不过。
//
//go:embed all:embedded
var embedded embed.FS

// ConsoleFS 返回内嵌的 Console 静态资源，根目录即 index.html 所在目录。
//
// 前端未构建时返回的文件系统里没有 index.html。调用方必须据此拒绝启动，
// 而不是让用户拿到一个只会 404 的界面。
func ConsoleFS() (fs.FS, error) {
	return fs.Sub(embedded, distDir)
}
