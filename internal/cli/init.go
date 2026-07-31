package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

// 每个路径的处置结果，直接作为输出里的一列。
const (
	actionCreated   = "已创建"
	actionExisting  = "已存在"
	actionTightened = "已存在，权限已收紧为 "
)

func runInit(args []string, options Options) error {
	set := newFlagSet("init", "创建配置目录与数据目录。重复执行不覆盖任何已有文件。", options)
	configDir := configDirFlag(set)
	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}

	dir := *configDir
	if dir == "" {
		resolved, resolveErr := config.Dir(nil)
		if resolveErr != nil {
			return resolveErr
		}
		dir = resolved
	}

	// 先建目录再写文件：配置文件必须落在 0700 的目录里，否则同机其他用户读得到它。
	layout := []struct {
		label  string
		path   string
		ensure func(string) (string, error)
	}{
		{"配置目录", dir, ensureDirectory},
		{"数据目录", filepath.Join(dir, config.DataDirName), ensureDirectory},
		{"配置文件", filepath.Join(dir, config.FileName), ensureConfigFile},
	}

	report := make([]string, 0, len(layout)+1)
	for _, entry := range layout {
		action, ensureErr := entry.ensure(entry.path)
		if ensureErr != nil {
			return ensureErr
		}
		report = append(report, fmt.Sprintf("%s  %s  %s", entry.label, entry.path, action))
	}

	// 令牌本身不打印：它是 Console 访问 /v1 的凭证，终端与滚屏历史都不该留着它。
	_, action, err := config.EnsureSessionToken(dir)
	if err != nil {
		return err
	}
	report = append(report, fmt.Sprintf("会话令牌  %s  %s",
		filepath.Join(dir, config.SessionTokenFileName), action))

	for _, line := range report {
		fmt.Fprintln(options.Stdout, line)
	}
	return nil
}

// ensureDirectory 建目录并把权限收紧到 0700。
//
// 已存在且权限过松时就地收紧，而不是报错：init 就是「把环境准备好」的命令，
// 只报错会让用户卡在 config.Load 的权限校验上，没有可执行的下一步。
func ensureDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if mkdirErr := os.MkdirAll(path, config.DirPermission); mkdirErr != nil {
			return "", apperr.Wrap(apperr.CodeInvalidConfiguration, mkdirErr).
				WithDetail("无法创建目录 " + path)
		}
		return actionCreated, nil
	}
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法读取 " + path)
	}
	if !info.IsDir() {
		return "", apperr.New(apperr.CodeInvalidConfiguration).WithDetail(path + " 已存在但不是目录")
	}
	return tighten(path, config.DirPermission)
}

// ensureConfigFile 在配置文件不存在时写入一份默认值。
//
// 已存在的文件绝不覆盖：里面是用户改过的偏好，init 重复执行不该把它们抹掉。
func ensureConfigFile(path string) (string, error) {
	content, err := json.MarshalIndent(config.Default(), "", "  ")
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("无法序列化默认配置")
	}

	// O_EXCL 而不是先 Stat 再写：两者之间的窗口足够让别人塞进一个符号链接。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, config.FilePermission) //nolint:gosec // 路径由配置目录约定拼出，不来自请求
	if errors.Is(err, fs.ErrExist) {
		return tighten(path, config.FilePermission)
	}
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法创建配置文件 " + path)
	}
	// 关闭错误与写入错误一起判断：文件系统把写失败留到 close 才报是常见情况，
	// 只看 Write 的返回值会漏掉半截的配置文件。
	_, writeErr := file.Write(append(content, '\n'))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法写入配置文件 " + path)
	}
	return actionCreated, nil
}

// tighten 把已存在的路径的权限收紧到 want，并说明做了什么。
func tighten(path string, want fs.FileMode) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).WithDetail("无法读取 " + path)
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志，
	// 在那里断言或修改 0700/0600 没有意义。本期 Windows 仅保证可构建。
	if runtime.GOOS == "windows" || info.Mode().Perm() == want {
		return actionExisting, nil
	}
	if err := os.Chmod(path, want); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法把 " + path + " 的权限收紧为 " + want.String())
	}
	return actionTightened + want.String(), nil
}
