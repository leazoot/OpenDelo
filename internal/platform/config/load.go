package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

const (
	// DirName 是配置目录在各平台基准目录下的名字。
	DirName = "opendelo"
	// FileName 是结构化偏好文件（PRD §0）。
	FileName = "config.json"
	// SessionTokenFileName 是 Console ↔ Gateway 的本地会话令牌（REQ-API-005）。
	SessionTokenFileName = "session_token"
	// DataDirName 是数据目录在配置目录下的名字。
	DataDirName = "data"

	// DirPermission 是配置目录与数据目录必须具备的权限。
	DirPermission fs.FileMode = 0o700
	// FilePermission 是敏感文件（会话令牌、数据库、配置文件）必须具备的权限。
	FilePermission fs.FileMode = 0o600
)

// DataDir 返回数据目录：配置目录下的 data/。
//
// 不另立 XDG_DATA_HOME：macOS 上配置与数据的基准目录本来就是同一个
// （~/Library/Application Support），分开解析只会在 Linux 上多一套逻辑，而两边
// 都仍然需要一层子目录，才不至于让 config.json 和 opendelo.db 混在一起。
func (c Config) DataDir() string { return filepath.Join(c.Dir, DataDirName) }

// 环境变量名。前缀避免与其他程序冲突。
const (
	envConfigHome      = "XDG_CONFIG_HOME"
	envListenAddress   = "OPENDELO_LISTEN_ADDRESS"
	envWebAPIPort      = "OPENDELO_WEB_API_PORT"
	envAgentProxyPort  = "OPENDELO_AGENT_PROXY_PORT"
	envMCPPort         = "OPENDELO_MCP_PORT"
	envLogLevel        = "OPENDELO_LOG_LEVEL"
	envAllowNonLoopbck = "OPENDELO_ALLOW_NON_LOOPBACK"
)

// Warning 是不阻止启动、但必须让用户看见的问题（REQ-PREF-001 AC3）。
// Message 已经是可直接展示的文本，不含文件内容。
type Warning struct {
	Path    string
	Message string
}

// LookupEnvFunc 与 os.LookupEnv 同形，注入它使环境变量相关的用例不必改动进程环境。
type LookupEnvFunc func(string) (string, bool)

// LoadParams 是 Load 的输入。
type LoadParams struct {
	// Dir 是配置目录。留空时按 XDG 约定解析（macOS 回落到 Application Support）。
	Dir string
	// Flags 是命令行上显式给出的值，优先级最高。
	Flags Overrides
	// LookupEnv 留空时使用 os.LookupEnv。
	LookupEnv LookupEnvFunc
}

// Load 按「命令行参数 > 环境变量 > 配置文件 > 默认值」的顺序合并配置并校验。
//
// 返回的 warnings 用于向用户展示已降级的部分；返回 error 表示拒绝启动 ——
// 目录或令牌权限不符、环境变量取值非法、合并结果不合法，都属于这一类。
// 配置文件损坏不在其中：那种情况回落到默认值并给出 warning（REQ-PREF-001 AC3）。
func Load(params LoadParams) (Config, []Warning, error) {
	lookupEnv := params.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	dir := params.Dir
	if dir == "" {
		resolved, err := Dir(lookupEnv)
		if err != nil {
			return Config{}, nil, err
		}
		dir = resolved
	}

	if err := checkDirectoryPermissions(dir); err != nil {
		return Config{}, nil, err
	}

	fromFile, warnings := readFile(filepath.Join(dir, FileName))

	fromEnv, err := readEnv(lookupEnv)
	if err != nil {
		return Config{}, warnings, err
	}

	merged := params.Flags.applyTo(fromEnv.applyTo(fromFile.applyTo(Default())))
	merged.Dir = dir

	if err := merged.Validate(); err != nil {
		return Config{}, warnings, err
	}
	return merged, warnings, nil
}

// Dir 返回配置目录：$XDG_CONFIG_HOME/opendelo，未设置时按平台回落
// （macOS 为 ~/Library/Application Support/opendelo）。
//
// 先看 XDG 而不是直接用 os.UserConfigDir：后者在 macOS 上会忽略 XDG_CONFIG_HOME，
// 而架构要求 XDG 优先。
func Dir(lookupEnv LookupEnvFunc) (string, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	if base, set := lookupEnv(envConfigHome); set && base != "" {
		return filepath.Join(base, DirName), nil
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法确定用户配置目录，可设置 " + envConfigHome + " 指定")
	}
	return filepath.Join(base, DirName), nil
}

// checkDirectoryPermissions 校验配置目录 0700 与令牌文件 0600（REQ-API-005 AC3）。
// 目录尚不存在时不报错 —— 首次运行由 opendelo init 创建。
func checkDirectoryPermissions(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法读取配置目录 " + dir)
	}
	if !info.IsDir() {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(dir + " 不是目录")
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志，
	// 断言 0700/0600 在那里没有意义。本期 Windows 仅保证可构建。
	if runtime.GOOS == "windows" {
		return nil
	}

	if permission := info.Mode().Perm(); permission != DirPermission {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(
			"配置目录 " + dir + " 的权限是 " + permission.String() + "，必须是 " + DirPermission.String(),
		)
	}

	return checkSessionTokenPermissions(filepath.Join(dir, SessionTokenFileName))
}

func checkSessionTokenPermissions(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法读取会话令牌文件 " + path)
	}
	if permission := info.Mode().Perm(); permission != FilePermission {
		return apperr.New(apperr.CodeInvalidConfiguration).WithDetail(
			"会话令牌 " + path + " 的权限是 " + permission.String() + "，必须是 " + FilePermission.String(),
		)
	}
	return nil
}

// readFile 读取 config.json。文件不存在是正常情况；读不动或解析失败一律回落到
// 「本层没有意见」并给出 warning，绝不因此崩溃或拒绝启动。
func readFile(path string) (Overrides, []Warning) {
	content, err := os.ReadFile(path) //nolint:gosec // 路径由配置目录约定拼出，不来自请求
	if errors.Is(err, fs.ErrNotExist) {
		return Overrides{}, nil
	}
	if err != nil {
		return Overrides{}, []Warning{{Path: path, Message: "配置文件无法读取，已使用默认值启动。"}}
	}

	var fromFile Overrides
	if err := json.Unmarshal(content, &fromFile); err != nil {
		// 只带上位置，不带文件内容 —— 损坏的文件里可能是任何东西。
		return Overrides{}, []Warning{{Path: path, Message: "配置文件无法解析" + syntaxPosition(err) + "，已使用默认值启动。"}}
	}
	return fromFile, nil
}

func syntaxPosition(err error) string {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return "（第 " + strconv.FormatInt(syntaxErr.Offset, 10) + " 字节处）"
	}
	return ""
}

// readEnv 读取环境变量层。
//
// 取值非法直接拒绝启动，不像配置文件那样回落：环境变量是用户此刻显式给出的输入，
// 悄悄忽略它会让人以为设置生效了。
func readEnv(lookupEnv LookupEnvFunc) (Overrides, error) {
	var fromEnv Overrides

	if raw, set := lookupEnv(envListenAddress); set {
		fromEnv.ListenAddress = &raw
	}
	if raw, set := lookupEnv(envLogLevel); set {
		fromEnv.LogLevel = &raw
	}

	ports := []struct {
		name  string
		field **int
	}{
		{name: envWebAPIPort, field: &fromEnv.WebAPIPort},
		{name: envAgentProxyPort, field: &fromEnv.AgentProxyPort},
		{name: envMCPPort, field: &fromEnv.MCPPort},
	}
	for _, port := range ports {
		raw, set := lookupEnv(port.name)
		if !set {
			continue
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Overrides{}, apperr.Wrap(apperr.CodeInvalidConfiguration, err).
				WithDetail(port.name + " 不是整数：" + strconv.Quote(raw))
		}
		*port.field = &parsed
	}

	if raw, set := lookupEnv(envAllowNonLoopbck); set {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return Overrides{}, apperr.Wrap(apperr.CodeInvalidConfiguration, err).
				WithDetail(envAllowNonLoopbck + " 不是布尔值：" + strconv.Quote(raw))
		}
		fromEnv.AllowNonLoopback = &parsed
	}

	return fromEnv, nil
}
