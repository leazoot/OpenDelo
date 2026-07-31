package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

// newConfigDir 返回一个尚不存在的配置目录路径。
//
// t.TempDir() 本身权限是 0700，直接拿来用会让「init 是否真的建了目录」无从判断。
func newConfigDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "opendelo")
}

func permissionOf(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return info.Mode().Perm()
}

func TestInit_CreatesDirectoriesAndConfigWithTightPermissions(t *testing.T) {
	dir := newConfigDir(t)
	got := execute(t, t.Context(), "init", "--config-dir", dir)

	if got.code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr 为 %q", got.code, got.stderr)
	}

	dataDir := filepath.Join(dir, config.DataDirName)
	configFile := filepath.Join(dir, config.FileName)

	for _, path := range []string{dir, dataDir, configFile} {
		if !strings.Contains(got.stdout, path) {
			t.Errorf("输出里没有 %s：%q", path, got.stdout)
		}
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志，
	// 断言 0700/0600 在那里没有意义（测试规则允许的平台限制）。
	if runtime.GOOS == "windows" {
		return
	}
	for path, want := range map[string]os.FileMode{
		dir:        config.DirPermission,
		dataDir:    config.DirPermission,
		configFile: config.FilePermission,
	} {
		if permission := permissionOf(t, path); permission != want {
			t.Errorf("%s 的权限是 %v，期望 %v", path, permission, want)
		}
	}
}

func TestInit_WrittenConfigIsLoadable(t *testing.T) {
	// init 写出来的文件必须是 config.Load 认得的形状，否则下一条命令就起不来。
	dir := newConfigDir(t)
	if got := execute(t, t.Context(), "init", "--config-dir", dir); got.code != cli.ExitOK {
		t.Fatalf("init 失败：%q", got.stderr)
	}

	content, err := os.ReadFile(filepath.Join(dir, config.FileName)) //nolint:gosec // 路径由用例构造
	if err != nil {
		t.Fatalf("读取配置文件失败：%v", err)
	}

	var written config.Config
	if unmarshalErr := json.Unmarshal(content, &written); unmarshalErr != nil {
		t.Fatalf("配置文件不是合法 JSON：%v", unmarshalErr)
	}
	written.Dir = dir
	if validateErr := written.Validate(); validateErr != nil {
		t.Errorf("init 写出的配置不合法：%v", validateErr)
	}

	loaded, warnings, err := config.Load(config.LoadParams{Dir: dir})
	if err != nil {
		t.Fatalf("加载 init 写出的配置失败：%v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("加载时产生了降级警告：%+v", warnings)
	}
	if loaded.WebAPIPort != config.Default().WebAPIPort {
		t.Errorf("端口为 %d，期望默认值 %d", loaded.WebAPIPort, config.Default().WebAPIPort)
	}
}

func TestInit_RunTwice_IsIdempotentAndKeepsEdits(t *testing.T) {
	// 第二次执行不能把用户改过的偏好抹掉。
	dir := newConfigDir(t)
	if got := execute(t, t.Context(), "init", "--config-dir", dir); got.code != cli.ExitOK {
		t.Fatalf("第一次 init 失败：%q", got.stderr)
	}

	configFile := filepath.Join(dir, config.FileName)
	edited := []byte(`{"web_api_port": 9000}`)
	if err := os.WriteFile(configFile, edited, config.FilePermission); err != nil {
		t.Fatalf("改写配置文件失败：%v", err)
	}

	second := execute(t, t.Context(), "init", "--config-dir", dir)
	if second.code != cli.ExitOK {
		t.Fatalf("第二次 init 失败：%q", second.stderr)
	}
	if !strings.Contains(second.stdout, "已存在") {
		t.Errorf("第二次执行未说明目录与文件已存在：%q", second.stdout)
	}

	after, err := os.ReadFile(configFile) //nolint:gosec // 路径由用例构造
	if err != nil {
		t.Fatalf("读取配置文件失败：%v", err)
	}
	if string(after) != string(edited) {
		t.Errorf("配置文件被覆盖了：%q", string(after))
	}
}

func TestInit_LoosePermissions_AreTightened(t *testing.T) {
	// 权限过松时就地收紧而不是报错：只报错会让用户卡在 config.Load 的校验上，
	// 没有可执行的下一步。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 ACL 与 Unix 权限位语义不同，本用例无意义")
	}

	dir := newConfigDir(t)
	if got := execute(t, t.Context(), "init", "--config-dir", dir); got.code != cli.ExitOK {
		t.Fatalf("init 失败：%q", got.stderr)
	}

	configFile := filepath.Join(dir, config.FileName)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("放松目录权限失败：%v", err)
	}
	if err := os.Chmod(configFile, 0o644); err != nil {
		t.Fatalf("放松文件权限失败：%v", err)
	}

	again := execute(t, t.Context(), "init", "--config-dir", dir)
	if again.code != cli.ExitOK {
		t.Fatalf("重新 init 失败：%q", again.stderr)
	}
	if !strings.Contains(again.stdout, "权限已收紧") {
		t.Errorf("输出未说明权限被收紧：%q", again.stdout)
	}
	if permission := permissionOf(t, dir); permission != config.DirPermission {
		t.Errorf("目录权限为 %v，期望 %v", permission, config.DirPermission)
	}
	if permission := permissionOf(t, configFile); permission != config.FilePermission {
		t.Errorf("文件权限为 %v，期望 %v", permission, config.FilePermission)
	}
}

func TestInit_ConfigDirIsAFile_IsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opendelo")
	if err := os.WriteFile(path, []byte("not a directory"), config.FilePermission); err != nil {
		t.Fatalf("准备文件失败：%v", err)
	}

	got := execute(t, t.Context(), "init", "--config-dir", path)
	if got.code == cli.ExitOK {
		t.Fatal("配置目录是个文件时 init 却成功了")
	}
	if !strings.Contains(got.stderr, "不是目录") {
		t.Errorf("stderr 未说明原因：%q", got.stderr)
	}
}
