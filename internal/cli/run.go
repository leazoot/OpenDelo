package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

/*
 * opendelo run —— 在清理过的环境里启动 Agent（REQ-CLI-001/002、PRD §21 L1）。
 *
 * 本命令**不**给子进程任何凭据，也不给它任何会话。它做的是相反的事：
 * 把父进程环境里的凭据拿掉，再把网关的位置放进去。子进程因此只剩一条
 * 拿到能力的路 —— 经网关请求，而那条路每次都要过决策。
 */

// exitCodeError 让子进程的退出码原样成为 opendelo 的退出码。
//
// 子进程失败了而 opendelo 报 1，会让 CI 里的 `opendelo run -- make test`
// 丢掉真实的失败原因。
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string {
	return "子进程以退出码 " + fmt.Sprint(e.code) + " 结束"
}

// listFlag 收集可重复出现的参数：--clear-env A --clear-env B。
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			*l = append(*l, trimmed)
		}
	}
	return nil
}

func runRun(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("run", "在清理过凭据的环境里启动 Agent：opendelo run -- claude", options)
	configDir := configDirFlag(set)
	verbose := set.Bool("verbose", false, "打印被清理的变量名与注入的网关地址（不含任何值）")

	var clearExtra, keep listFlag
	set.Var(&clearExtra, "clear-env", "额外要清理的环境变量名，可重复或用逗号分隔")
	set.Var(&keep, "keep-env", "明确保留的环境变量名，可重复或用逗号分隔")

	command, forwarded := splitCommand(args)
	proceed, err := parse(set, command, options)
	if err != nil || !proceed {
		return err
	}
	if len(forwarded) == 0 {
		return apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有要启动的命令。用法：opendelo run -- <命令> [参数]")
	}

	settings, warnings, err := config.Load(config.LoadParams{Dir: *configDir})
	if err != nil {
		return err
	}
	printWarnings(options.Stderr, warnings)

	sessionToken, err := config.SessionToken(settings.Dir)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(settings.ListenAddress, strconv.Itoa(settings.WebAPIPort))

	// 先建会话再启动子进程：凭证要放进它的环境，而环境在它启动之后就改不动了。
	session, err := openSession(ctx, address, sessionToken, forwarded, options.Clock.Now())
	if err != nil {
		return err
	}

	// 环境清理是 L1 的手段之一（REQ-GATEWAY-005 AC2、PRD §21）。
	// L0 只代理与审计，不动 Agent 的环境 —— 那正是 L0 与 L1 的区别所在。
	plan := planEnviron(os.Environ(), settings, clearExtra, keep, session.key)
	if *verbose {
		reportPlan(options.Stderr, plan, settings)
	}

	spawnErr := spawn(ctx, forwarded, plan.Environ, options)
	return endSession(ctx, address, sessionToken, session, spawnErr, *verbose, options)
}

// endSession 结束会话，并决定这次命令最终返回哪个错误。
//
// 子进程的退出码优先（REQ-CLI-001 AC1）：把它换成「断开会话失败」会让
// `opendelo run -- make test` 丢掉真实的失败原因。断开失败因此报到 stderr 而不是
// 顶掉退出码 —— 但它绝不被丢掉，会话没断开意味着一批授权还活着。
func endSession(
	ctx context.Context, address, sessionToken string, session agentSession,
	spawnErr error, verbose bool, options Options,
) error {
	revoked, err := closeSession(ctx, address, sessionToken, session)
	switch {
	case err != nil && spawnErr == nil:
		return err
	case err != nil:
		fmt.Fprintf(options.Stderr,
			"子进程已退出，但结束会话失败：%v\n会话 %s 仍然有效，用 Console 或 opendelo revoke 收回它名下的授权。\n",
			err, session.agentID)
	case verbose:
		fmt.Fprintf(options.Stderr, "已结束会话 %s，收回 %d 条到任务结束的授权\n",
			session.agentID, revoked)
	}
	return spawnErr
}

// splitCommand 把 `--` 之前的参数留给 flag，之后的原样交给子进程。
//
// 不这样切的话 `opendelo run -- claude --verbose` 里的 --verbose 会被
// opendelo 自己吃掉。
func splitCommand(args []string) (own, forwarded []string) {
	for index, arg := range args {
		if arg == "--" {
			return args[:index], args[index+1:]
		}
	}
	return args, nil
}

// reportPlan 打印这次改写做了什么。
//
// 只打印变量名，不打印任何值 —— 被清理的那些变量的值正是凭据本身
// （REQ-CLI-003 AC2：--verbose 下同样不输出凭据）。
func reportPlan(out io.Writer, plan environPlan, settings config.Config) {
	if len(plan.Removed) == 0 {
		fmt.Fprintln(out, "环境中没有需要清理的凭据变量")
	} else {
		fmt.Fprintf(out, "已清理 %d 个凭据变量：%s\n",
			len(plan.Removed), strings.Join(plan.Removed, " "))
	}
	// 只遍历 proxyEnvironment：会话凭证也在注入之列，但它的值不能出现在任何输出里
	// （REQ-CLI-003 AC2）。
	for _, name := range sortedKeys(proxyEnvironment(settings)) {
		fmt.Fprintf(out, "注入 %s=%s\n", name, proxyEnvironment(settings)[name])
	}
	fmt.Fprintf(out, "注入 %s（值不打印）\n", sessionKeyVariable)
}

// spawn 启动子进程并把它的退出码带回来。
func spawn(ctx context.Context, forwarded, environ []string, options Options) error {
	// 命令由用户在 `--` 之后显式给出，正是本命令的用途。用的是参数数组形式，
	// 不经 shell，因此不存在拼接注入。
	child := exec.CommandContext(ctx, forwarded[0], forwarded[1:]...) //nolint:gosec // G204
	child.Env = environ
	child.Stdin = os.Stdin
	child.Stdout = options.Stdout
	child.Stderr = options.Stderr

	err := child.Run()
	if err == nil {
		return nil
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitCodeError{code: exit.ExitCode()}
	}
	return apperr.Wrap(apperr.CodeInvalidRequest, err).
		WithDetail("启动 " + forwarded[0] + " 失败")
}
