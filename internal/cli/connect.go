package cli

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/config"
)

/*
 * opendelo connect / revoke（REQ-CLI-001）。
 *
 * 两条命令都只做参数解析与输出格式化，判断在网关那一侧。
 *
 * connect 收的是一份**凭据引用**（provider + item + field 登记之后拿到的 ID），
 * 不是凭据本身。命令行参数会进 shell 历史与进程列表，明文一旦从这里进来，
 * 后面清理得再干净也已经晚了。
 */

func runConnect(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("connect",
		"用一份已登记的凭据引用建立一个身份：opendelo connect --credential-reference <id>", options)
	configDir := configDirFlag(set)
	reference := set.String("credential-reference", "", "凭据引用 ID（不是凭据本身）")
	label := set.String("account-label", "", "账号标签，例如 acme-bot")
	environment := set.String("environment", "production", "production 或 non-production")
	isDefault := set.Bool("default", false, "设为该服务的默认身份")

	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}
	if *reference == "" {
		return apperr.New(apperr.CodeInvalidRequest).
			WithDetail("缺少 --credential-reference。先用 Console 登记凭据来源，再用拿到的引用 ID 连接身份。")
	}

	address, sessionToken, err := gatewayEndpoint(*configDir, options)
	if err != nil {
		return err
	}

	identity, err := gatewayclient.ConnectIdentity(ctx, address, sessionToken,
		gatewayclient.ConnectRequest{
			CredentialReferenceID: *reference,
			AccountLabel:          *label,
			Environment:           *environment,
			IsDefault:             *isDefault,
		})
	if err != nil {
		return err
	}

	fmt.Fprintf(options.Stdout, "已连接  %s\n服务    %s\n账号    %s\n环境    %s\n状态    %s\n",
		identity.ID, identity.Service, identity.AccountLabel,
		identity.Environment, identity.Status)
	return nil
}

func runRevoke(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("revoke", "收回一条 Lease：opendelo revoke --lease <id>", options)
	configDir := configDirFlag(set)
	leaseID := set.String("lease", "", "要收回的 Lease ID")

	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}
	if *leaseID == "" {
		return apperr.New(apperr.CodeInvalidRequest).
			WithDetail("缺少 --lease。用 opendelo status 或 Console 查看生效中的 Lease。")
	}

	address, sessionToken, err := gatewayEndpoint(*configDir, options)
	if err != nil {
		return err
	}

	revoked, err := gatewayclient.RevokeLease(ctx, address, sessionToken, *leaseID)
	if err != nil {
		return err
	}

	fmt.Fprintf(options.Stdout, "已收回  %s\n服务    %s\n状态    %s\n原到期  %s\n",
		revoked.ID, revoked.Service, revoked.Status, revoked.ExpiresAt)
	return nil
}

// gatewayEndpoint 解析配置并取出会话令牌，是三条要连网关的命令共用的开头。
func gatewayEndpoint(configDir string, options Options) (address, sessionToken string, err error) {
	settings, warnings, err := config.Load(config.LoadParams{Dir: configDir})
	if err != nil {
		return "", "", err
	}
	printWarnings(options.Stderr, warnings)

	sessionToken, err = config.SessionToken(settings.Dir)
	if err != nil {
		return "", "", err
	}
	return net.JoinHostPort(settings.ListenAddress, strconv.Itoa(settings.WebAPIPort)), sessionToken, nil
}
