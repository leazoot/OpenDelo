package onepassword

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * 1Password Provider：经 op CLI 读取一个字段（REQ-CRED-002）。
 *
 * 三条硬约束：
 *
 *  1. **只读用户显式选择的那一个字段**。引用被逐段校验，任何指向整个条目或
 *     整个保险库的写法都拒绝 —— 「导出整个 Vault」在这里连表达都表达不出来。
 *  2. **参数以数组传递，永不经 shell**。用户给的保险库名、条目名、字段名
 *     都是独立参数，不进入 shell 解析。
 *  3. **明文不缓存**。取出来的字节在包装成 secret.Value 之后立刻清零中间缓冲，
 *     本包不留任何副本。
 */

// DefaultBinary 是 op CLI 的默认名字，从 PATH 里找。
const DefaultBinary = "op"

// DefaultTimeout 是一次 op 调用的上限。
//
// 桌面授权模式下 op 会等用户在 1Password 里点确认，所以不能太短；
// 但也必须有上限 —— 一次永远等下去的取用会把整条请求挂住。
const DefaultTimeout = 30 * time.Second

// Options 是 Source 的配置。
type Options struct {
	// Binary 为空时用 DefaultBinary。
	Binary string
	// Runner 为空时走 os/exec。
	Runner registry.CommandRunner
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
}

// Source 是 1Password 凭据来源。
type Source struct {
	binary  string
	runner  registry.CommandRunner
	timeout time.Duration
}

// New 构造 1Password 来源。
func New(options Options) *Source {
	source := &Source{
		binary:  options.Binary,
		runner:  options.Runner,
		timeout: options.Timeout,
	}
	if source.binary == "" {
		source.binary = DefaultBinary
	}
	if source.runner == nil {
		source.runner = execRunner{}
	}
	if source.timeout <= 0 {
		source.timeout = DefaultTimeout
	}
	return source
}

func (s *Source) Kind() registry.ProviderKind { return registry.KindOnePassword }

// Available 探测 op 是否可用。
//
// 用 `op --version`：它不读任何条目，也不会触发解锁提示 ——
// 一次健康探测不该在用户屏幕上弹出授权窗口。
func (s *Source) Available(ctx context.Context) error {
	if _, err := s.run(ctx, []string{"--version"}); err != nil {
		return err
	}
	return nil
}

// Fetch 读出一个字段的明文。
//
// 只发一条 `op read op://<vault>/<item>/<field>`：这个子命令一次只返回一个字段，
// 不存在「顺手把整个条目也拿回来」的形态（REQ-CRED-002 的禁止项）。
func (s *Source) Fetch(
	ctx context.Context, reference registry.Reference,
) (secret.Value, error) {
	uri, err := readURI(reference)
	if err != nil {
		return secret.Value{}, err
	}

	output, err := s.run(ctx, []string{"read", "--no-newline", uri})
	if err != nil {
		return secret.Value{}, err
	}
	// op 在部分版本下仍会带一个结尾换行，去掉它再包装。
	trimmed := bytes.TrimRight(output, "\r\n")
	if len(trimmed) == 0 {
		zero(output)
		return secret.Value{}, apperr.New(apperr.CodeCredentialNotAuthorized).
			WithDetail("1Password 字段 " + reference.Field + " 是空的")
	}

	// trimmed 与 output 共用同一段内存，secret.New 接管的正是它的前缀
	// （`platform/secret` 不复制，复制会在堆上留下第二份清不掉的明文）。
	// 因此只清掉尾部那几个被裁掉的字节；前缀的清零由 Value.Zero 负责。
	zero(output[len(trimmed):])
	return secret.New(trimmed), nil
}

func (s *Source) run(ctx context.Context, args []string) ([]byte, error) {
	timed, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	output, err := s.runner.Run(timed, s.binary, args)
	if err == nil {
		return output, nil
	}

	var typed *apperr.Error
	switch {
	case errors.Is(timed.Err(), context.DeadlineExceeded):
		// 桌面授权模式下这通常意味着用户没有在 1Password 里点确认。
		return nil, apperr.Wrap(apperr.CodeProviderLockedTimeout, err).
			WithDetail("等待 1Password 授权超时")
	case errors.As(err, &typed):
		// 解析可执行文件时已经给出了确切原因，不要用一句通用文案盖掉它。
		return nil, typed
	case errors.Is(err, exec.ErrNotFound):
		return nil, apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("找不到 1Password CLI（op）")
	default:
		// 未登录、Service Account Token 失效、条目不存在都落在这里。
		// 对调用方来说后果一样：取不到凭据，请求必须被拒（AC3）。
		return nil, apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("1Password CLI 调用失败")
	}
}

// readURI 把引用拼成 op:// 读取地址，并逐段校验。
//
// 校验的目的不是「拼得对」，而是**拼不出一个比用户选的更宽的地址**：
// 少一段就成了整个条目，多一个通配符就成了一批条目，而 op 对这两种写法
// 都会照单全收（REQ-CRED-002 AC1）。
func readURI(reference registry.Reference) (string, error) {
	const scheme = "op://"

	if !strings.HasPrefix(reference.ItemRef, scheme) {
		return "", notAuthorized("引用不是 op:// 地址")
	}
	segments := strings.Split(strings.TrimPrefix(reference.ItemRef, scheme), "/")
	if len(segments) != 2 {
		return "", notAuthorized("引用必须精确到一个条目：op://<保险库>/<条目>")
	}

	field := reference.Field
	for _, segment := range append(segments, field) {
		if segment == "" {
			return "", notAuthorized("引用里有空的段")
		}
		if strings.ContainsAny(segment, "*?") {
			return "", notAuthorized("引用里不允许出现通配符")
		}
		if strings.Contains(segment, "..") {
			return "", notAuthorized("引用里不允许出现 ..")
		}
		if strings.ContainsAny(segment, "\n\r") {
			return "", notAuthorized("引用里不允许出现换行")
		}
	}
	if strings.Contains(field, "/") {
		return "", notAuthorized("字段名里不允许出现斜杠")
	}

	return reference.ItemRef + "/" + field, nil
}

func notAuthorized(detail string) error {
	return apperr.New(apperr.CodeCredentialNotAuthorized).WithDetail(detail)
}

// zero 覆盖一段缓冲。明文离开本包之后，这里不该再留下任何可读的字节。
func zero(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}

// execRunner 是默认实现：参数以数组传递，永不经 shell。
type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string) ([]byte, error) {
	// 先把名字定死成一个别人改不了的绝对路径：默认值 "op" 是从 PATH 里找的，
	// 而 Agent 有文件系统访问权，能往 PATH 里放一个同名文件顶替掉它。
	resolved, err := registry.ResolveBinary(binary)
	if err != nil {
		return nil, err
	}

	// 参数数组形式：用户给的保险库名、条目名、字段名都是独立参数，
	// 不进入 shell 解析（命令注入防护）。
	//
	//nolint:gosec // G204 报的是「用变量启动子进程」。这里正是本 Provider 的职责：
	// 可执行文件已由 ResolveBinary 定死成一个其他用户不可改写的绝对路径，
	// 参数以数组传递、永不经 shell，且每一段都已由 readURI 逐段校验。
	// 用例用一个塞满 shell 元字符的条目名端到端验证过这条路径不会被解释。
	command := exec.CommandContext(ctx, resolved, args...)
	// 不继承环境：op 的凭据由它自己的会话或 Service Account Token 提供，
	// 而本进程的环境里可能有别的服务的令牌，没有理由传给它。
	command.Env = []string{}

	var stdout bytes.Buffer
	command.Stdout = &stdout
	// 丢弃 stderr：op 的报错文本可能包含条目名与路径，进不了日志更安全。
	command.Stderr = nil

	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
