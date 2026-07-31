package config_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/config"
)

// preparedDir 返回一个权限正确、内容为空的配置目录。
func preparedDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "opendelo")
	if err := os.Mkdir(dir, config.DirPermission); err != nil {
		t.Fatalf("创建配置目录失败：%v", err)
	}
	return dir
}

func tokenPath(dir string) string {
	return filepath.Join(dir, config.SessionTokenFileName)
}

func TestEnsureSessionToken_FirstRun_CreatesUnpredictableTokenWith0600(t *testing.T) {
	// REQ-API-005 AC3。
	dir := preparedDir(t)

	token, action, err := config.EnsureSessionToken(dir)
	if err != nil {
		t.Fatalf("生成会话令牌失败：%v", err)
	}
	if action != config.SessionFileCreated {
		t.Errorf("处置结果为 %q，期望 %q", action, config.SessionFileCreated)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("令牌不是 base64url：%v", err)
	}
	// 32 字节的熵：暴力猜测在本机回环上也是不可行的。
	if len(raw) != 32 {
		t.Errorf("令牌有 %d 字节熵，期望 32", len(raw))
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志。
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(tokenPath(dir))
	if err != nil {
		t.Fatalf("读取令牌文件失败：%v", err)
	}
	if permission := info.Mode().Perm(); permission != config.FilePermission {
		t.Errorf("令牌文件权限为 %v，期望 %v", permission, config.FilePermission)
	}
}

func TestEnsureSessionToken_TwoDirectories_GetDifferentTokens(t *testing.T) {
	// 令牌若可预测，Origin 校验之外的那道防线就形同虚设。
	first, _, err := config.EnsureSessionToken(preparedDir(t))
	if err != nil {
		t.Fatalf("生成第一个令牌失败：%v", err)
	}
	second, _, err := config.EnsureSessionToken(preparedDir(t))
	if err != nil {
		t.Fatalf("生成第二个令牌失败：%v", err)
	}
	if first == second {
		t.Error("两次生成得到了同一个令牌")
	}
}

func TestEnsureSessionToken_SecondRun_ReturnsTheSameToken(t *testing.T) {
	// 令牌每次启动都变的话，已经打开的 Console 会突然全部 401。
	dir := preparedDir(t)

	created, _, err := config.EnsureSessionToken(dir)
	if err != nil {
		t.Fatalf("生成会话令牌失败：%v", err)
	}
	again, action, err := config.EnsureSessionToken(dir)
	if err != nil {
		t.Fatalf("再次读取会话令牌失败：%v", err)
	}
	if again != created {
		t.Error("第二次拿到的令牌变了")
	}
	if action != config.SessionFileExisting {
		t.Errorf("处置结果为 %q，期望 %q", action, config.SessionFileExisting)
	}
}

func TestEnsureSessionToken_LoosePermissions_RegeneratesInsteadOfTightening(t *testing.T) {
	// 一个可能已经被同机其他用户读走的令牌，收紧权限并不能让它重新变得可信。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 ACL 与 Unix 权限位语义不同，本用例无意义")
	}

	dir := preparedDir(t)
	leaked, _, err := config.EnsureSessionToken(dir)
	if err != nil {
		t.Fatalf("生成会话令牌失败：%v", err)
	}
	if chmodErr := os.Chmod(tokenPath(dir), 0o644); chmodErr != nil {
		t.Fatalf("放松令牌文件权限失败：%v", chmodErr)
	}

	replaced, action, err := config.EnsureSessionToken(dir)
	if err != nil {
		t.Fatalf("重新生成会话令牌失败：%v", err)
	}
	if replaced == leaked {
		t.Error("权限异常的令牌被原样保留了")
	}
	if action != config.SessionFileRegenerated {
		t.Errorf("处置结果为 %q，期望 %q", action, config.SessionFileRegenerated)
	}

	info, err := os.Stat(tokenPath(dir))
	if err != nil {
		t.Fatalf("读取令牌文件失败：%v", err)
	}
	if permission := info.Mode().Perm(); permission != config.FilePermission {
		t.Errorf("重新生成后权限为 %v，期望 %v", permission, config.FilePermission)
	}
}

func TestEnsureSessionToken_EmptyFile_IsRejected(t *testing.T) {
	// 空令牌等于「谁都能通过校验」，必须拒绝而不是接受一个空字符串。
	dir := preparedDir(t)
	if err := os.WriteFile(tokenPath(dir), nil, config.FilePermission); err != nil {
		t.Fatalf("写空令牌文件失败：%v", err)
	}

	if _, _, err := config.EnsureSessionToken(dir); err == nil {
		t.Fatal("空令牌文件被接受了")
	}
}

func TestSessionToken_Missing_TellsUserToRunInit(t *testing.T) {
	_, err := config.SessionToken(preparedDir(t))
	if err == nil {
		t.Fatal("令牌不存在却没有报错")
	}
	if !strings.Contains(err.Error(), "opendelo init") {
		t.Errorf("错误信息未给出可执行的下一步：%v", err)
	}
}

func TestSessionToken_DoesNotCreateAnything(t *testing.T) {
	// 只读操作不该有副作用：status 探测一次不该顺手生成一个令牌。
	dir := preparedDir(t)

	if _, err := config.SessionToken(dir); err == nil {
		t.Fatal("令牌不存在却没有报错")
	}
	if _, err := os.Stat(tokenPath(dir)); err == nil {
		t.Error("只读的 SessionToken 生成了令牌文件")
	}
}
