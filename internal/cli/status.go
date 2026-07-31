package cli

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

func runStatus(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("status", "探测本机 Gateway 并输出状态。未运行时退出码非 0。", options)
	configDir := configDirFlag(set)
	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
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
	status, err := gatewayclient.Probe(ctx, address, sessionToken)
	if err != nil {
		return err
	}

	// 地址是「实际连上的那个」，版本与启动时间是 Gateway 自己报的：
	// 前者回答「我连的是谁」，后者回答「它是什么」。
	fmt.Fprintf(options.Stdout, "Gateway  %s\n地址     %s\n版本     %s\n启动于   %s\n",
		status.Status, address, status.Version, status.StartedAt)
	return nil
}
