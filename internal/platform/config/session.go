package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// sessionTokenBytes 是会话令牌的熵。
//
// 32 字节远超暴力猜测的可行范围；令牌只在本机回环上使用，再长没有意义。
const sessionTokenBytes = 32

// 会话令牌**文件**的处置结果，供 CLI 如实告诉用户发生了什么。名字里不带 Token：
// gosec 的 G101 会把带该词的字符串常量当成硬编码凭据。
const (
	SessionFileCreated     = "已创建"
	SessionFileExisting    = "已存在"
	SessionFileRegenerated = "权限异常，已重新生成"
)

// EnsureSessionToken 返回配置目录下的会话令牌，必要时生成一个。
//
// 令牌是 Console 访问 /v1 的唯一凭证（REQ-API-005）。文件权限必须是 0600；
// 权限过松时**重新生成**而不是就地收紧 —— 一个可能已经被同机其他用户读走的令牌，
// 收紧权限并不能让它重新变得可信。
//
// 返回值的第二项说明这次做了什么，只用于展示，不含令牌本身。
func EnsureSessionToken(dir string) (string, string, error) {
	path := filepath.Join(dir, SessionTokenFileName)

	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		token, writeErr := writeSessionToken(path)
		return token, SessionFileCreated, writeErr
	case err != nil:
		return "", "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法读取会话令牌文件 " + path)
	case !info.Mode().IsRegular():
		return "", "", apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail(path + " 不是普通文件")
	}

	// Windows 的 ACL 与 Unix 权限位语义不同，os.FileMode 只反映只读标志。
	// 本期 Windows 仅保证可构建。
	if runtime.GOOS != "windows" && info.Mode().Perm() != FilePermission {
		if removeErr := os.Remove(path); removeErr != nil {
			return "", "", apperr.Wrap(apperr.CodeInvalidConfiguration, removeErr).
				WithDetail("无法删除权限异常的会话令牌 " + path)
		}
		token, writeErr := writeSessionToken(path)
		return token, SessionFileRegenerated, writeErr
	}

	token, err := readSessionToken(path)
	return token, SessionFileExisting, err
}

// SessionToken 读取已存在的会话令牌。
//
// 与 EnsureSessionToken 分开：探测状态这类只读操作不该产生副作用，而且「令牌还没
// 生成」本身就说明还没 init 过，直接说出来比默默补一个更有用。
//
// 文件权限由 Load 校验（REQ-API-005 AC3），这里只负责取值。
func SessionToken(dir string) (string, error) {
	path := filepath.Join(dir, SessionTokenFileName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("还没有会话令牌（" + path + "），先运行 opendelo init")
	}
	return readSessionToken(path)
}

func readSessionToken(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // 路径由配置目录约定拼出，不来自请求
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法读取会话令牌文件 " + path)
	}
	// 内容不进错误信息：它就是令牌本身。
	token := string(content)
	if token == "" {
		return "", apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("会话令牌文件 " + path + " 是空的，删除它后重新运行 opendelo init")
	}
	return token, nil
}

func writeSessionToken(path string) (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		// 拿不到随机数就没有可信的令牌，绝不退回到可预测的来源。
		return "", apperr.Wrap(apperr.CodeInternal, err).WithDetail("无法生成会话令牌")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	// O_EXCL 而不是先 Stat 再写：两者之间的窗口足够让别人塞进一个符号链接，
	// 令牌就会被写到别处去。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, FilePermission) //nolint:gosec // 同上
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法创建会话令牌文件 " + path)
	}
	_, writeErr := file.WriteString(token)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("无法写入会话令牌文件 " + path)
	}
	return token, nil
}
