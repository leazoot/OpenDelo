// Package cli 实现 opendelo 的命令行界面。
//
// 只做参数解析、编排与输出格式化。任何业务判断都在被调用的包里，本包不做决策。
// 输出中不得出现凭据（REQ-CLI-003），由 test/sentinel 的 CLI 面扫描守住。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

// 退出码语义：0 成功，非 0 失败（REQ-CLI-001 AC1）。
const (
	ExitOK      = 0
	ExitFailure = 1
)

// Options 是运行 CLI 所需的一切。
//
// IO 与时钟都注入进来，使用例既不必改动进程状态，也不必等真实时间。
type Options struct {
	Args    []string
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	Clock   clock.Clock
}

// Run 执行一条命令并返回进程退出码。
func Run(ctx context.Context, options Options) int {
	if len(options.Args) == 0 {
		printUsage(options.Stdout, options.Version)
		return ExitOK
	}

	name, args := options.Args[0], options.Args[1:]

	var err error
	switch name {
	case "init":
		err = runInit(args, options)
	case "start":
		err = runStart(ctx, args, options)
	case "status":
		err = runStatus(ctx, args, options)
	case "run":
		err = runRun(ctx, args, options)
	case "connect":
		err = runConnect(ctx, args, options)
	case "revoke":
		err = runRevoke(ctx, args, options)
	case "connections":
		err = runConnections(ctx, args, options)
	case "leases":
		err = runLeases(ctx, args, options)
	case "audit":
		err = runAudit(ctx, args, options)
	case "version", "-version", "--version":
		_, err = fmt.Fprintln(options.Stdout, options.Version)
	case "help", "-h", "-help", "--help":
		printUsage(options.Stdout, options.Version)
	default:
		fmt.Fprintf(options.Stderr, "未知命令 %q\n\n", name)
		printUsage(options.Stderr, options.Version)
		return ExitFailure
	}

	if err != nil {
		// 子进程的退出码原样带出去，不折成 1：那会让 opendelo run 包住的
		// 命令丢掉真实的失败原因。
		var exit exitCodeError
		if errors.As(err, &exit) {
			return exit.code
		}
		fmt.Fprintln(options.Stderr, err)
		return ExitFailure
	}
	return ExitOK
}

func printUsage(out io.Writer, version string) {
	fmt.Fprintf(out, `opendelo %s — Agent 身份、凭据与行动网关

用法：
  opendelo <命令> [参数]

命令：
  init      创建配置目录与数据目录
  start     在前台启动 Gateway
  status    探测本机 Gateway 并输出状态
  run       在清理过凭据的环境里启动 Agent
  connect   用一份已登记的凭据引用建立一个身份
  revoke    收回一条 Lease
  connections  列出已连接的身份
  leases       列出生效中的 Lease
  audit        列出账本记录
  version   打印版本号

用 opendelo <命令> --help 查看单个命令的参数。
`, version)
}

// newFlagSet 构造一条命令的参数集，并把用法写成统一格式。
//
// 解析错误写到 stderr，用法写到 stdout：前者是失败，后者是正常输出，
// 混在一个流里会让 `opendelo init --help > doc.txt` 这类用法拿不到内容。
func newFlagSet(name, summary string, options Options) *flag.FlagSet {
	set := flag.NewFlagSet("opendelo "+name, flag.ContinueOnError)
	set.SetOutput(options.Stderr)
	set.Usage = func() {
		fmt.Fprintf(set.Output(), "%s\n\n用法：\n  opendelo %s [参数]\n\n参数：\n", summary, name)
		set.PrintDefaults()
	}
	return set
}

// parse 在解析前拦下 -h / --help。
//
// flag 包自己处理 -h 时会把用法写到它的输出流，而那个流是给错误用的；
// 拦下来才能把用法送到 stdout。返回 false 表示这条命令已经处理完了。
func parse(set *flag.FlagSet, args []string, options Options) (bool, error) {
	for _, arg := range args {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			set.SetOutput(options.Stdout)
			set.Usage()
			return false, nil
		}
	}
	if err := set.Parse(args); err != nil {
		return false, err
	}
	return true, nil
}

// configDirFlag 是三条命令共用的参数：指定配置目录，留空按平台约定解析。
func configDirFlag(set *flag.FlagSet) *string {
	return set.String("config-dir", "", "配置目录，留空时按平台约定解析")
}

// printWarnings 把配置降级的提示写到 stderr，不影响命令本身的输出。
func printWarnings(out io.Writer, warnings []config.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(out, "警告：%s（%s）\n", warning.Message, warning.Path)
	}
}
