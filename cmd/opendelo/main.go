// Command opendelo 是 OpenDelo Gateway 的唯一入口，同时承担 CLI 与常驻进程两种形态。
//
// 本文件只做进程生命周期：接线信号、把退出码交给 os.Exit。参数解析与命令实现在
// internal/cli。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/clock"
)

// version 由构建时的 -ldflags "-X main.version=..." 覆盖，默认值用于源码直接运行的场景。
var version = "0.0.0-dev"

func main() {
	// 退出码经 run 返回而不是在里面直接 os.Exit：那样 defer 不会执行，信号注册也就撤不掉。
	os.Exit(run())
}

func run() int {
	// SIGINT / SIGTERM 转成 context 取消，让 start 走优雅关闭而不是被直接杀掉。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Run(ctx, cli.Options{
		Args:    os.Args[1:],
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
		Clock:   clock.System{},
	})
}
