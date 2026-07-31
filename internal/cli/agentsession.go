package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * `opendelo run` 的会话两端（REQ-CLI-002 AC3、REQ-AGENT-001）。
 *
 * 九项身份绑定由 run 而不是 Agent 提交。理由是威胁模型：Agent 可能被提示注入，
 * 让它自报可执行文件哈希与进程号等于没有绑定。run 是它的父进程，这些事实
 * run 全都看得到。
 *
 * 会话在子进程退出后立即断开，名下「允许到任务结束」的 Lease 一并收回 ——
 * 那正是「到任务结束」这句话的意思。断开无条件执行，包括子进程被信号打断时。
 */

// hashPrefix 标明摘要算法，与 agentauth 对 Session Key 哈希的写法一致，
// 使日后换算法时旧值仍可辨认。
const hashPrefix = "sha256:"

// maxExecutableBytes 是计算哈希时读取的上限。
//
// 给一个上限而不是无界读：`opendelo run -- <某个巨大的文件>` 不该把内存吃光。
// 超过上限时哈希只覆盖前 512 MiB —— 但那已经远大于任何真实的 Agent 二进制，
// 走到这一步说明用户指的根本不是一个程序。
const maxExecutableBytes = 512 << 20

// agentSession 是一次已建立的会话。
type agentSession struct {
	agentID string
	// key 是明文 Session Key。它只从这里流向子进程的环境，不写日志、不写文件。
	key string
}

// openSession 替即将启动的子进程注册一次会话。
//
// 拿不到会话就返回错误而不是继续：子进程的环境里已经没有任何凭据，
// 网关又不认识它，此时启动它只会得到一个每次请求都 407 的 Agent，
// 而失败的原因早已滚出屏幕。
func openSession(
	ctx context.Context, address, sessionToken string, forwarded []string, now time.Time,
) (agentSession, error) {
	registration, err := describeAgent(forwarded, now)
	if err != nil {
		return agentSession{}, err
	}

	registered, err := gatewayclient.RegisterAgent(ctx, address, sessionToken, registration)
	if err != nil {
		return agentSession{}, err
	}
	if registered.SessionKey == "" {
		return agentSession{}, apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail("网关没有签发会话凭证")
	}
	return agentSession{agentID: registered.Agent.ID, key: registered.SessionKey}, nil
}

// closeSession 结束会话。
//
// 用 context.WithoutCancel：子进程通常是被 Ctrl-C 一起打断的，那时 ctx 已经取消，
// 而「取消了就不收回授权」正好是最不该发生的事。
func closeSession(
	ctx context.Context, address, sessionToken string, session agentSession,
) (int, error) {
	revocation, err := gatewayclient.DisconnectAgent(
		context.WithoutCancel(ctx), address, sessionToken, session.agentID)
	if err != nil {
		return 0, err
	}
	return revocation.RevokedLeases, nil
}

// describeAgent 收集九项身份绑定。
//
// PID 与 ParentPID 记的是 **`opendelo run` 自己**，不是子进程：子进程的环境
// 必须在它启动之前就固定下来，而会话凭证要放进那份环境里 —— 于是注册只能
// 发生在子进程存在之前。run 与它的子进程同生共死，用 run 的进程号描述这次会话
// 是准确的；描述的是哪个程序则由可执行文件路径与哈希回答，那两项是子进程的。
func describeAgent(forwarded []string, now time.Time) (gatewayclient.RegisterRequest, error) {
	executable, err := exec.LookPath(forwarded[0])
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("找不到要启动的命令 " + forwarded[0])
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("无法解析 " + forwarded[0] + " 的绝对路径")
	}

	digest, err := fileDigest(executable)
	if err != nil {
		return gatewayclient.RegisterRequest{}, err
	}

	osUser, err := user.Current()
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法确定当前系统用户")
	}

	workspace, err := os.Getwd()
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法确定当前工作目录")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法解析工作目录的绝对路径")
	}

	hostname, err := os.Hostname()
	if err != nil {
		return gatewayclient.RegisterRequest{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法确定本机名称")
	}

	name := filepath.Base(executable)
	return gatewayclient.RegisterRequest{
		Name:               name,
		Type:               string(agentTypeOf(name)),
		ExecutableHash:     digest,
		ExecutablePath:     executable,
		PID:                os.Getpid(),
		ParentPID:          os.Getppid(),
		OSUser:             osUser.Username,
		DeviceFingerprint:  deviceFingerprint(hostname),
		DeviceName:         hostname,
		WorkspacePath:      workspace,
		ProjectFingerprint: projectFingerprint(workspace),
		StartedAt:          now.UTC().Format(time.RFC3339),
	}, nil
}

// agentTypeOf 按可执行文件名判断接入类型。
//
// 类型只用于展示，不参与身份判定（见 agentauth.AgentType），所以认不出来时
// 落到 generic 是安全的 —— 它不会让任何判断变宽松。
func agentTypeOf(name string) agentType {
	switch strings.ToLower(strings.TrimSuffix(name, ".exe")) {
	case "claude":
		return typeClaudeCode
	case "codex":
		return typeCodex
	case "gemini":
		return typeGeminiCLI
	case "opencode":
		return typeOpenCode
	default:
		return typeGeneric
	}
}

// agentType 与 agentauth.AgentType 的取值一一对应。
//
// 这里重新声明而不是 import core：CLI 走的是 HTTP 契约，取值是契约的一部分，
// 让它经过 core 的类型会让「CLI 直接调 core」看起来是允许的。
type agentType string

const (
	typeClaudeCode agentType = "claude-code"
	typeCodex      agentType = "codex"
	typeGeminiCLI  agentType = "gemini-cli"
	typeOpenCode   agentType = "opencode"
	typeGeneric    agentType = "generic"
)

// fileDigest 计算可执行文件的 SHA-256。
//
// 它是「还是当初那个程序吗」的判据：换了一个二进制就匹配不到原记录，
// 于是新建一条 Agent 记录，旧的授权记忆留在旧记录上（REQ-AGENT-001 AC2）。
func fileDigest(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // G304：路径来自用户显式给出的命令，正是本命令的用途
	if err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("无法读取 " + path)
	}
	defer func() { _ = file.Close() }()

	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, maxExecutableBytes)); err != nil {
		return "", apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("无法读取 " + path)
	}
	return hashPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

// deviceFingerprint 是这台机器跨重启稳定的标识。
//
// 由主机名与平台推出而不是随机生成后落盘：本期 Gateway 只监听回环（假设 A-04），
// 「哪台机器」这个问题在一台机器的范围内只有一个答案，不需要为它引入一个
// 要管理生命周期的状态文件。连接远程 Gateway 时这一处要重做。
func deviceFingerprint(hostname string) string {
	return digestOf("opendelo-device", hostname, runtime.GOOS, runtime.GOARCH)
}

// projectFingerprint 是「还是当初那个项目吗」的判据（REQ-IDENT-003 AC3）。
//
// 取工作区路径加上 .git/config 的内容：同一个目录被重新指向另一个仓库时，
// remote 变了，指纹随之变化，依赖该项目的绑定需要重新确认。读不到 .git/config
// （不是 git 仓库、或没有读权限）时只按路径计算 —— 那不会放宽任何判断，
// 只是让这一维暂时不起作用。
//
// 文件内容只参与哈希，不被存储也不被打印：远端 URL 里可能带着凭据。
func projectFingerprint(workspace string) string {
	// G304：路径由当前工作目录推出，且只参与哈希 —— 内容既不返回也不记录。
	gitConfig, err := os.ReadFile(filepath.Join(workspace, ".git", "config")) //nolint:gosec
	if err != nil {
		return digestOf("opendelo-project", workspace)
	}
	return digestOf("opendelo-project", workspace, string(gitConfig))
}

// digestOf 用 NUL 分隔各段后取 SHA-256。
//
// 分隔符不能是空串：那样 ("ab","c") 与 ("a","bc") 会得到同一个指纹。
func digestOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hashPrefix + hex.EncodeToString(sum[:])
}
