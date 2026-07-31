package web_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Runcoor/opendelo/web"
)

func TestConsoleFS_RootIsTheDistDirectoryItself(t *testing.T) {
	// fs.Sub 已经剥掉 dist 前缀：Console 的资源路径来自 Vite 的 base（/assets/...），
	// 多一层前缀会让每个资源都 404。
	console, err := web.ConsoleFS()
	if err != nil {
		t.Fatalf("取内嵌资源失败：%v", err)
	}
	if _, err := fs.Stat(console, "dist"); err == nil {
		t.Error("资源根目录下仍有 dist 一层")
	}
}

func TestEmbedPlaceholder_Exists(t *testing.T) {
	// embedded/.gitkeep 是 go:embed 在前端尚未构建时唯一能匹配到的文件。删掉它，
	// 干净检出的仓库连编译都过不去 —— 这条用例就是那颗钉子。
	//
	// 它放在 Vite 的 outDir 之外：outDir 每次构建都会被清空，占位文件放进去构建一次就没了。
	if _, err := os.Stat(filepath.Join("embedded", ".gitkeep")); err != nil {
		t.Errorf("占位文件不存在：%v", err)
	}
}

// 构建产物本身的检查（是否有内联脚本、资源是否自洽）在 scripts/check-csp.mjs：
// 它由 make web-check 在 web-build 之后运行，那里 dist 一定存在。放在 Go 用例里
// 只能写成「没构建就跳过」，等于没检查。
