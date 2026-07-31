package registry

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// ResolveBinary 把一个可执行文件名解析成绝对路径，并校验它不会被别的用户改写。
//
// 为什么需要它：本产品的威胁模型假设 Agent 不可信，而 Agent 有文件系统访问权。
// 「往 PATH 里放一个同名的 op 顶替掉真的 op」因此是一条真实存在的路径 ——
// 顶替成功就等于把明文凭据交到 Agent 手里。
//
// 这里挡不住全部情况（能写进 PATH 目录的攻击者往往已经能以该用户身份执行代码），
// 但保证两件事：跑的是一个确定的绝对路径，而不是每次调用重新在 PATH 里碰运气；
// 且这个文件不是任何其他用户可以改写的。
//
// 不校验属主：Homebrew 装的 op 属于当前用户而不是 root，要求 root 属主会把
// 绝大多数真实安装判成不可用 —— 那是把可用性问题伪装成安全收益。
func ResolveBinary(name string) (string, error) {
	located, err := exec.LookPath(name)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("找不到可执行文件 " + name)
	}

	// 相对路径的含义取决于进程当时的工作目录 —— 那不是一个确定的可执行文件，
	// 按 Fail Closed 一律拒绝，而不是替用户猜一个当前目录出来。
	if !filepath.IsAbs(located) {
		return "", apperr.New(apperr.CodeProviderUnavailable).
			WithDetail("可执行文件必须写绝对路径：" + located)
	}

	// LookPath 已经保证它不是目录且带执行位，这里只补上「别人能不能改写它」。
	info, err := os.Stat(located)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("无法读取 " + located + " 的文件信息")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", apperr.New(apperr.CodeProviderUnavailable).
			WithDetail(located + " 可被其他用户改写，拒绝执行")
	}

	return located, nil
}
