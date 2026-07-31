package keychain

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * macOS Keychain Provider：经 security CLI 读一条 Generic / Internet Password
 * （REQ-CRED-003）。
 *
 * 走命令行而不是 cgo：ADR-006 要求保持跨平台单二进制，而 Keychain 的 C API
 * 一旦引入就要为整个项目开启 cgo。security(1) 是系统自带的，能力也正好够 ——
 * 它一次只返回一条目的一个密码，没有「导出整个钥匙串」的调用形态。
 *
 * 非 macOS 上本 Provider 不可用（AC2）。判断走一个可注入的字段而不是编译标签：
 * 这样「在 Linux 上会怎样」在 macOS 的机器上也测得到，反之亦然。
 */

// DefaultBinary 是 security CLI 的默认路径。
//
// 写绝对路径而不是名字：这是系统自带的可执行文件，从 PATH 里找会让
// 一个同名的、放在 PATH 前面的文件顶替掉它。
const DefaultBinary = "/usr/bin/security"

// DefaultTimeout 是一次 security 调用的上限。
//
// Touch ID 解锁要等用户按指纹，所以不能太短；但也必须有上限 ——
// 一次永远等下去的取用会把整条请求挂住。
const DefaultTimeout = 30 * time.Second

// ItemKind 是钥匙串条目的种类（REQ-CRED-003）。
type ItemKind string

const (
	// KindGeneric 是 Generic Password，对应 security find-generic-password。
	KindGeneric ItemKind = "generic"
	// KindInternet 是 Internet Password，对应 security find-internet-password。
	KindInternet ItemKind = "internet"
)

// Options 是 Source 的配置。
type Options struct {
	// Binary 为空时用 DefaultBinary。
	Binary string
	// Runner 为空时走 os/exec。
	Runner registry.CommandRunner
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// GOOS 为空时取 runtime.GOOS。用例用它验证非 macOS 上的行为。
	GOOS string
}

// Source 是 macOS Keychain 凭据来源。
type Source struct {
	binary  string
	runner  registry.CommandRunner
	timeout time.Duration
	goos    string
}

// New 构造 Keychain 来源。
func New(options Options) *Source {
	source := &Source{
		binary:  options.Binary,
		runner:  options.Runner,
		timeout: options.Timeout,
		goos:    options.GOOS,
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
	if source.goos == "" {
		source.goos = runtime.GOOS
	}
	return source
}

func (s *Source) Kind() registry.ProviderKind { return registry.KindMacOSKeychain }

// Available 探测本 Provider 能不能用。
//
// 先看平台再看可执行文件：在 Linux 上问 security 的版本没有意义，
// 而返回一个「找不到可执行文件」会把「这个平台不支持」说成「装一下就好了」。
func (s *Source) Available(ctx context.Context) error {
	if err := s.supported(); err != nil {
		return err
	}
	if _, err := s.run(ctx, []string{"help"}); err != nil {
		return err
	}
	return nil
}

// Fetch 读出一条钥匙串条目的密码。
//
// `-w` 让 security 只打印密码本身，不打印条目的其余属性 ——
// 没有它，输出里会带上创建时间、访问组等元信息，而那些不该进本进程的内存。
func (s *Source) Fetch(
	ctx context.Context, reference registry.Reference,
) (secret.Value, error) {
	if err := s.supported(); err != nil {
		return secret.Value{}, err
	}

	kind, service, err := parseItemRef(reference.ItemRef)
	if err != nil {
		return secret.Value{}, err
	}
	account := reference.Field
	if accountErr := checkSegment(account); accountErr != nil {
		return secret.Value{}, accountErr
	}

	command := "find-generic-password"
	if kind == KindInternet {
		command = "find-internet-password"
	}

	output, err := s.run(ctx, []string{command, "-s", service, "-a", account, "-w"})
	if err != nil {
		return secret.Value{}, err
	}

	trimmed := bytes.TrimRight(output, "\r\n")
	if len(trimmed) == 0 {
		zero(output)
		return secret.Value{}, apperr.New(apperr.CodeCredentialNotAuthorized).
			WithDetail("钥匙串条目 " + service + " 的密码是空的")
	}

	// trimmed 与 output 共用同一段内存，secret.New 接管的正是它的前缀。
	// 只清尾部被裁掉的字节；前缀的清零由 Value.Zero 负责。
	zero(output[len(trimmed):])
	return secret.New(trimmed), nil
}

// supported 是 AC2：非 macOS 上本 Provider 不可用。
func (s *Source) supported() error {
	if s.goos != "darwin" {
		return apperr.New(apperr.CodeProviderNotSupportedOnPlatform).
			WithDetail("macOS Keychain 在 " + s.goos + " 上不可用")
	}
	return nil
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
		// 通常意味着用户没有按 Touch ID，或没有在解锁提示上点允许。
		return nil, apperr.Wrap(apperr.CodeProviderLockedTimeout, err).
			WithDetail("等待钥匙串解锁超时")
	case errors.As(err, &typed):
		// 解析可执行文件时已经给出了确切原因，不要用一句通用文案盖掉它。
		return nil, typed
	case errors.Is(err, exec.ErrNotFound):
		return nil, apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("找不到 security 命令")
	default:
		// 条目不存在、用户拒绝授权、钥匙串已锁都落在这里。
		// 对调用方来说后果一样：取不到凭据，请求必须被拒。
		return nil, apperr.Wrap(apperr.CodeProviderUnavailable, err).
			WithDetail("security 命令调用失败")
	}
}

// parseItemRef 解析 keychain://<种类>/<服务> 并校验。
//
// 与 1Password 那边同一个道理：校验的目的是**拼不出一个比用户选的更宽的查询**。
// security 的 -s 参数支持模糊匹配，一个带通配符的服务名会命中一批条目。
func parseItemRef(itemRef string) (ItemKind, string, error) {
	const scheme = "keychain://"

	if !strings.HasPrefix(itemRef, scheme) {
		return "", "", notAuthorized("引用不是 keychain:// 地址")
	}
	segments := strings.SplitN(strings.TrimPrefix(itemRef, scheme), "/", 2)
	if len(segments) != 2 {
		return "", "", notAuthorized("引用必须精确到一个服务：keychain://<种类>/<服务>")
	}

	kind := ItemKind(segments[0])
	if kind != KindGeneric && kind != KindInternet {
		return "", "", notAuthorized("条目种类只能是 generic 或 internet")
	}
	if err := checkSegment(segments[1]); err != nil {
		return "", "", err
	}
	return kind, segments[1], nil
}

// checkSegment 拦下会让查询变宽或跑到别处的写法。
func checkSegment(segment string) error {
	switch {
	case segment == "":
		return notAuthorized("引用里有空的段")
	case strings.ContainsAny(segment, "*?"):
		return notAuthorized("引用里不允许出现通配符")
	case strings.ContainsAny(segment, "\n\r"):
		return notAuthorized("引用里不允许出现换行")
	case strings.HasPrefix(segment, "-"):
		// 以 - 开头的值会被 security 当成选项，而不是它前面那个选项的取值。
		return notAuthorized("引用里的值不能以 - 开头")
	default:
		return nil
	}
}

func notAuthorized(detail string) error {
	return apperr.New(apperr.CodeCredentialNotAuthorized).WithDetail(detail)
}

// zero 覆盖一段缓冲。
func zero(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}

// execRunner 是默认实现：参数以数组传递，永不经 shell。
type execRunner struct{}

func (execRunner) Run(ctx context.Context, binary string, args []string) ([]byte, error) {
	// 默认值已经是系统自带的绝对路径，但 Binary 可被配置覆盖 ——
	// 覆盖进来的值同样要过这一关。
	resolved, err := registry.ResolveBinary(binary)
	if err != nil {
		return nil, err
	}

	//nolint:gosec // G204 报的是「用变量启动子进程」。这里正是本 Provider 的职责：
	// 可执行文件已由 ResolveBinary 定死成一个其他用户不可改写的绝对路径，
	// 参数以数组传递、永不经 shell，且服务名与账户名都已由 checkSegment 逐段校验。
	command := exec.CommandContext(ctx, resolved, args...)
	// 不继承环境：security 从当前用户的登录会话取钥匙串，不需要本进程的环境，
	// 而本进程的环境里可能有别的服务的令牌。
	command.Env = []string{}

	var stdout bytes.Buffer
	command.Stdout = &stdout
	// 丢弃 stderr：security 的报错文本会带上条目名。
	command.Stderr = nil

	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
