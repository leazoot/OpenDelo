package config_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

func emptyEnv(string) (string, bool) { return "", false }

func envFrom(values map[string]string) config.LookupEnvFunc {
	return func(name string) (string, bool) {
		value, set := values[name]
		return value, set
	}
}

// configDir 建一个权限合规的临时配置目录。
func configDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("设置目录权限失败：%v", err)
	}
	return dir
}

func writeConfigFile(t *testing.T, dir, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("写入配置文件失败：%v", err)
	}
}

func loadFrom(t *testing.T, params config.LoadParams) config.Config {
	t.Helper()

	if params.LookupEnv == nil {
		params.LookupEnv = emptyEnv
	}
	loaded, warnings, err := config.Load(params)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("出现预期外的告警：%+v", warnings)
	}
	return loaded
}

func TestDefault_UsesDocumentedPortsAndLoopback(t *testing.T) {
	// AC1：PRD §7.2 规定的三个端口，默认只监听回环。
	defaults := config.Default()

	if defaults.ListenAddress != "127.0.0.1" {
		t.Errorf("ListenAddress = %q，期望 127.0.0.1", defaults.ListenAddress)
	}
	for _, port := range []struct {
		name string
		got  int
		want int
	}{
		{name: "WebAPIPort", got: defaults.WebAPIPort, want: 8787},
		{name: "AgentProxyPort", got: defaults.AgentProxyPort, want: 8788},
		{name: "MCPPort", got: defaults.MCPPort, want: 8789},
	} {
		if port.got != port.want {
			t.Errorf("%s = %d，期望 %d", port.name, port.got, port.want)
		}
	}
	if defaults.AllowNonLoopback {
		t.Error("AllowNonLoopback 默认为 true，非回环监听必须默认关闭")
	}
	if err := defaults.Validate(); err != nil {
		t.Errorf("默认配置未通过校验：%v", err)
	}
}

func TestLoad_PriorityOrder_FlagsBeatEnvBeatFileBeatDefaults(t *testing.T) {
	// AC3：四层各一个用例，每层只覆盖比它低的层。
	const (
		filePort  = 19001
		envPort   = 19002
		flagsPort = 19003
	)

	cases := []struct {
		name     string
		withFile bool
		env      map[string]string
		flags    config.Overrides
		want     int
	}{
		{name: "四层都没给时用默认值", want: config.Default().WebAPIPort},
		{name: "配置文件覆盖默认值", withFile: true, want: filePort},
		{
			name:     "环境变量覆盖配置文件",
			withFile: true,
			env:      map[string]string{"OPENDELO_WEB_API_PORT": "19002"},
			want:     envPort,
		},
		{
			name:     "命令行参数覆盖环境变量",
			withFile: true,
			env:      map[string]string{"OPENDELO_WEB_API_PORT": "19002"},
			flags:    config.Overrides{WebAPIPort: intPtr(flagsPort)},
			want:     flagsPort,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := configDir(t)
			if testCase.withFile {
				writeConfigFile(t, dir, `{"web_api_port": 19001}`)
			}

			loaded := loadFrom(t, config.LoadParams{
				Dir:       dir,
				Flags:     testCase.flags,
				LookupEnv: envFrom(testCase.env),
			})

			if loaded.WebAPIPort != testCase.want {
				t.Errorf("WebAPIPort = %d，期望 %d", loaded.WebAPIPort, testCase.want)
			}
		})
	}
}

func TestLoad_PartialFile_LeavesOtherFieldsAtDefault(t *testing.T) {
	dir := configDir(t)
	writeConfigFile(t, dir, `{"log_level": "debug"}`)

	loaded := loadFrom(t, config.LoadParams{Dir: dir})

	if loaded.LogLevel != config.LevelDebug {
		t.Errorf("LogLevel = %q，期望 debug", loaded.LogLevel)
	}
	if loaded.WebAPIPort != config.Default().WebAPIPort {
		t.Errorf("未在文件中出现的 WebAPIPort 被改成了 %d", loaded.WebAPIPort)
	}
	if loaded.Dir != dir {
		t.Errorf("Dir = %q，期望 %q", loaded.Dir, dir)
	}
}

func TestLoad_CorruptFile_FallsBackToDefaultsWithWarning(t *testing.T) {
	// AC2：损坏的配置文件不得让进程崩溃，也不得阻止启动。
	cases := map[string]string{
		"截断的 JSON":  `{"web_api_port": 19001`,
		"根不是对象":     `[1, 2, 3]`,
		"字段类型不对":    `{"web_api_port": "不是数字"}`,
		"完全不是 JSON": "这不是 JSON",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := configDir(t)
			writeConfigFile(t, dir, body)

			loaded, warnings, err := config.Load(config.LoadParams{Dir: dir, LookupEnv: emptyEnv})
			if err != nil {
				t.Fatalf("损坏的配置文件不应阻止启动，却返回了：%v", err)
			}
			if loaded.WebAPIPort != config.Default().WebAPIPort {
				t.Errorf("WebAPIPort = %d，期望回落到默认值 %d", loaded.WebAPIPort, config.Default().WebAPIPort)
			}
			if len(warnings) != 1 {
				t.Fatalf("产生了 %d 条告警，期望 1 条：%+v", len(warnings), warnings)
			}
			if warnings[0].Message == "" {
				t.Error("告警没有可展示的说明")
			}
			if strings.Contains(warnings[0].Message, body) {
				t.Errorf("告警里带上了文件内容：%s", warnings[0].Message)
			}
		})
	}
}

func TestLoad_MissingDirectoryOrFile_UsesDefaultsWithoutWarning(t *testing.T) {
	// 首次运行是正常情况，不该吓唬用户。
	loaded := loadFrom(t, config.LoadParams{Dir: filepath.Join(t.TempDir(), "还没建")})

	if loaded.WebAPIPort != config.Default().WebAPIPort {
		t.Errorf("WebAPIPort = %d，期望默认值", loaded.WebAPIPort)
	}
}

func TestLoad_DirectoryPermissionNot0700_RefusesToStart(t *testing.T) {
	// AC4。Windows 的 ACL 与 Unix 权限位语义不同，见 checkDirectoryPermissions。
	skipOnWindows(t)

	for _, permission := range []os.FileMode{0o755, 0o777, 0o750} {
		t.Run(permission.String(), func(t *testing.T) {
			dir := configDir(t)
			if err := os.Chmod(dir, permission); err != nil {
				t.Fatalf("设置目录权限失败：%v", err)
			}
			// 三个权限位都保留了 owner 的 rwx，t.TempDir 的清理不受影响。

			_, _, err := config.Load(config.LoadParams{Dir: dir, LookupEnv: emptyEnv})
			assertInvalidConfiguration(t, err)
		})
	}
}

func TestLoad_SessionTokenPermissionNot0600_RefusesToStart(t *testing.T) {
	skipOnWindows(t)

	dir := configDir(t)
	tokenPath := filepath.Join(dir, config.SessionTokenFileName)
	if err := os.WriteFile(tokenPath, []byte("不是真令牌"), 0o644); err != nil { //nolint:gosec // 用例故意放宽权限
		t.Fatalf("写入令牌文件失败：%v", err)
	}

	_, _, err := config.Load(config.LoadParams{Dir: dir, LookupEnv: emptyEnv})
	assertInvalidConfiguration(t, err)

	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatalf("修正令牌权限失败：%v", err)
	}
	if _, _, err := config.Load(config.LoadParams{Dir: dir, LookupEnv: emptyEnv}); err != nil {
		t.Errorf("权限修正后仍然拒绝启动：%v", err)
	}
}

func TestValidate_NonLoopbackListenAddress_RequiresExplicitConfirmation(t *testing.T) {
	cases := []struct {
		name             string
		address          string
		allowNonLoopback bool
		wantRejected     bool
	}{
		{name: "回环 IPv4 默认允许", address: "127.0.0.1"},
		{name: "回环 IPv6 默认允许", address: "::1"},
		{name: "全零地址未确认时拒绝", address: "0.0.0.0", wantRejected: true},
		{name: "局域网地址未确认时拒绝", address: "192.168.1.10", wantRejected: true},
		{name: "全零地址显式确认后允许", address: "0.0.0.0", allowNonLoopback: true},
		{name: "非法地址始终拒绝", address: "不是地址", allowNonLoopback: true, wantRejected: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := config.Default()
			candidate.ListenAddress = testCase.address
			candidate.AllowNonLoopback = testCase.allowNonLoopback

			err := candidate.Validate()
			if testCase.wantRejected {
				assertInvalidConfiguration(t, err)
				return
			}
			if err != nil {
				t.Errorf("期望通过校验，却返回：%v", err)
			}
		})
	}
}

func TestValidate_RejectsUnusablePortsAndLevels(t *testing.T) {
	cases := map[string]func(*config.Config){
		"端口为 0":     func(c *config.Config) { c.WebAPIPort = 0 },
		"端口超出范围":    func(c *config.Config) { c.MCPPort = 70000 },
		"端口为负":      func(c *config.Config) { c.AgentProxyPort = -1 },
		"两个接入面共用端口": func(c *config.Config) { c.AgentProxyPort = c.WebAPIPort },
		"三个接入面共用端口": func(c *config.Config) { c.MCPPort, c.AgentProxyPort = c.WebAPIPort, c.WebAPIPort },
		"日志级别不在枚举内": func(c *config.Config) { c.LogLevel = "trace" },
		"日志级别为空字符串": func(c *config.Config) { c.LogLevel = "" },
	}

	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := config.Default()
			corrupt(&candidate)

			assertInvalidConfiguration(t, candidate.Validate())
		})
	}
}

func TestLoad_InvalidEnvValue_RefusesToStart(t *testing.T) {
	// 环境变量是此刻显式给出的输入，悄悄忽略会让人以为设置生效了。
	cases := map[string]map[string]string{
		"端口不是整数":  {"OPENDELO_WEB_API_PORT": "八七八七"},
		"布尔值不合法":  {"OPENDELO_ALLOW_NON_LOOPBACK": "也许"},
		"日志级别不合法": {"OPENDELO_LOG_LEVEL": "trace"},
	}

	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := config.Load(config.LoadParams{Dir: configDir(t), LookupEnv: envFrom(env)})
			assertInvalidConfiguration(t, err)
		})
	}
}

func TestLoad_EnvAllowsNonLoopback(t *testing.T) {
	loaded := loadFrom(t, config.LoadParams{
		Dir: configDir(t),
		LookupEnv: envFrom(map[string]string{
			"OPENDELO_LISTEN_ADDRESS":     "0.0.0.0",
			"OPENDELO_ALLOW_NON_LOOPBACK": "true",
		}),
	})

	if loaded.ListenAddress != "0.0.0.0" || !loaded.AllowNonLoopback {
		t.Errorf("环境变量层未生效：%+v", loaded)
	}
}

func TestDir_PrefersXDGConfigHome(t *testing.T) {
	// 架构要求 XDG 优先；os.UserConfigDir 在 macOS 上会忽略它。
	dir, err := config.Dir(envFrom(map[string]string{"XDG_CONFIG_HOME": "/tmp/xdg-probe"}))
	if err != nil {
		t.Fatalf("解析目录失败：%v", err)
	}
	if want := filepath.Join("/tmp/xdg-probe", config.DirName); dir != want {
		t.Errorf("Dir() = %q，期望 %q", dir, want)
	}
}

func TestDir_WithoutXDG_FallsBackToPlatformDirectory(t *testing.T) {
	dir, err := config.Dir(emptyEnv)
	if err != nil {
		t.Fatalf("解析目录失败：%v", err)
	}
	if filepath.Base(dir) != config.DirName {
		t.Errorf("Dir() = %q，期望以 %q 结尾", dir, config.DirName)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(dir, "Application Support") {
		t.Errorf("macOS 上 Dir() = %q，期望回落到 Application Support", dir)
	}
}

func TestSlogLevel_MapsEveryConfiguredLevel(t *testing.T) {
	cases := map[string]slog.Level{
		config.LevelDebug: slog.LevelDebug,
		config.LevelInfo:  slog.LevelInfo,
		config.LevelWarn:  slog.LevelWarn,
		config.LevelError: slog.LevelError,
	}

	for name, want := range cases {
		candidate := config.Default()
		candidate.LogLevel = name
		if got := candidate.SlogLevel(); got != want {
			t.Errorf("%q 映射为 %v，期望 %v", name, got, want)
		}
	}
}

func assertInvalidConfiguration(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("期望被拒绝，却通过了")
	}
	if !apperr.Is(err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码不是 invalid_configuration：%v", err)
	}

	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("不是 *apperr.Error：%v", err)
	}
	if appErr.Error() == appErr.Public().Message {
		t.Error("错误没有带上可诊断的 detail")
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 ACL 与 Unix 权限位语义不同，权限校验在该平台上跳过")
	}
}

func intPtr(value int) *int { return &value }

func TestValidate_SecurityLevel_OnlyAcceptsTheTwoImplementedLevels(t *testing.T) {
	// REQ-GATEWAY-005：本期只有 L0 与 L1，L2 Isolated 不实现。
	// 认不出的取值一律拒绝启动 —— 把 "l1" 静默当成 L0 跑起来，
	// 用户会以为自己受着保护而其实没有。
	cases := map[string]bool{
		config.LevelEnforced: true,
		config.LevelRelay:    true,
		"":                   false,
		"l1":                 false,
		"L2":                 false,
		"enforced":           false,
	}
	for level, valid := range cases {
		t.Run("security_level="+level, func(t *testing.T) {
			candidate := config.Default()
			candidate.SecurityLevel = level

			err := candidate.Validate()
			if valid && err != nil {
				t.Errorf("%q 被拒绝了：%v", level, err)
			}
			if !valid && err == nil {
				t.Errorf("%q 被接受了", level)
			}
		})
	}
}
