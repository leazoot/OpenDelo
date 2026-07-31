package cli

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/model"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/gateway"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/credential/localvault"
	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * 组装根。
 *
 * 这是整个程序里唯一知道「谁由谁构造」的地方。它存在的理由是别处不该知道：
 * `core` 拿到的是已经装好的仓储，`transport` 拿到的是已经装好的 core，
 * 谁都不必去 import 数据库驱动，依赖方向因此能被 test/arch 静态地证明。
 *
 * 三条约束在这里成立：
 *
 *  1. **装不齐就不启动。** 每个构造器缺依赖时都返回错误，本文件一律向上传递。
 *     没有「先起来，用到再说」的路径 —— 那样第一个真实请求才会发现装错了，
 *     而那时凭据已经取出来了。
 *  2. **装配顺序即依赖顺序。** 数据库 → 仓储 → core → 接入面。
 *     反向依赖在这里写不出来，因为上一层的变量还不存在。
 *  3. **关闭是装配的一部分。** 每个打开的资源在 Gateway 的 Close 里有对应的关闭，
 *     由本文件同时给出，不留给调用方去记。
 */

// databaseFileName 是数据目录下的数据库文件名。
const databaseFileName = "opendelo.db"

// Gateway 是装配完成的一台网关：三个接入面所需的一切，加上关闭它们的办法。
type Gateway struct {
	// Availability 是网关此刻是否接受受保护请求（REQ-GATEWAY-003）。
	Availability *gateway.Availability
	// Web 是 Console 与 Web API 的接入面（8787）。
	Web *httpapi.Server
	// AgentProxy 是 Agent 出站请求的接入面（8788）。
	AgentProxy *faceServer
	// MCP 是 Agent 工具调用的接入面（8789）。
	MCP *faceServer

	db     *store.DB
	events *httpapi.Broker
}

// AssembleParams 是装配一台网关所需的外部输入。
type AssembleParams struct {
	Config       config.Config
	Clock        clock.Clock
	Logger       *slog.Logger
	Version      string
	SessionToken string
	Console      fs.FS
}

// Assemble 打开数据库、跑完迁移，装配 core 与接入面。
//
// 任何一步失败都会把已经打开的资源关掉再返回错误：一个装配失败却留着数据库
// 连接的进程，下一次启动会撞上被占用的文件而看不出原因。
func Assemble(ctx context.Context, params AssembleParams) (*Gateway, error) {
	database, err := store.Open(ctx, store.Options{Path: databasePath(params.Config)})
	if err != nil {
		return nil, err
	}

	assembled, err := assembleOn(ctx, database, params)
	if err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return assembled, nil
}

func assembleOn(ctx context.Context, database *store.DB, params AssembleParams) (*Gateway, error) {
	if _, err := store.Migrate(ctx, database); err != nil {
		return nil, err
	}

	ids := ulid.New(params.Clock)
	services, err := assembleServices(database, ids, params)
	if err != nil {
		return nil, err
	}
	services.Events = httpapi.NewBroker(params.Logger)

	// 注册表要在 8787 之前装好：`POST /v1/capability-requests` 与 MCP 工具调用
	// 走的是同一段输入装配，而那段要读 Adapter 的能力声明。
	registry, err := assembleAdapters()
	if err != nil {
		return nil, err
	}
	// Exchange 要在编排之前装好：执行前的查勘与执行走同一段凭据处理
	// （取出 → 注入 → 出站 → 清零），而两个 Agent 面共用的正是这一个。
	exchange, err := adapters.NewExchange(registry, services.Credentials,
		&credentialReferences{identities: services.Identities})
	if err != nil {
		return nil, err
	}
	decide, err := orchestration.New(orchestration.Submissions{
		Pipeline: services.Pipeline, Identities: repo.NewIdentities(database),
		Agents: repo.NewAgents(database), Devices: repo.NewDevices(database),
		Declarations: repo.NewServiceAdapters(database), Registry: registry,
		Previews: exchange, Requests: repo.NewCapabilityRequests(database),
		Clock: params.Clock, Logger: params.Logger,
	})
	if err != nil {
		return nil, err
	}
	services.Submissions = decide
	services.Capabilities = registry

	web, err := httpapi.New(httpapi.Options{
		Config:       params.Config,
		Console:      params.Console,
		Clock:        params.Clock,
		Logger:       params.Logger,
		Version:      params.Version,
		SessionToken: params.SessionToken,
		Services:     services,
	})
	if err != nil {
		return nil, err
	}

	availability := gateway.New()
	faces, err := assembleAgentFaces(agentFaceInputs{
		database: database, availability: availability, services: services,
		params: params, registry: registry, submissions: decide, exchange: exchange,
	})
	if err != nil {
		return nil, err
	}

	return &Gateway{
		Availability: availability,
		Web:          web,
		AgentProxy:   faces.proxy,
		MCP:          faces.mcp,
		db:           database,
		events:       services.Events,
	}, nil
}

// agentFaces 是两个 Agent 接入面。
type agentFaces struct {
	proxy *faceServer
	mcp   *faceServer
}

// assembleAgentFaces 装配 8788 与 8789。
//
// 两个面共用一份 Adapter 注册表、一个 Exchange 与一个账本写入器。共用不是为了
// 省对象：注册表决定「这台网关能做什么」，两份注册表就等于两个答案，而 MCP
// 清单上写着的能力与 Proxy 放行的路径必须是同一批。
func assembleAgentFaces(inputs agentFaceInputs) (agentFaces, error) {
	recorder, err := audit.NewRecorder(
		repo.NewAuditEvents(inputs.database), inputs.params.Clock, inputs.services.IDs)
	if err != nil {
		return agentFaces{}, err
	}

	shared := assembleFaceParams{
		availability: inputs.availability, services: inputs.services, params: inputs.params,
		registry: inputs.registry, exchange: inputs.exchange, recorder: recorder,
		declarations: repo.NewServiceAdapters(inputs.database), submissions: inputs.submissions,
	}
	agentProxy, err := assembleAgentProxy(shared)
	if err != nil {
		return agentFaces{}, err
	}
	mcp, err := assembleMCP(shared)
	if err != nil {
		return agentFaces{}, err
	}
	return agentFaces{proxy: agentProxy, mcp: mcp}, nil
}

// agentFaceInputs 是装配两个 Agent 面所需的外部输入。
type agentFaceInputs struct {
	database     *store.DB
	availability *gateway.Availability
	services     httpapi.Services
	params       AssembleParams
	registry     *adapters.Registry
	exchange     *adapters.Exchange
	submissions  *orchestration.Submissions
}

// assembleFaceParams 是两个 Agent 面共用的那部分依赖。
type assembleFaceParams struct {
	availability *gateway.Availability
	services     httpapi.Services
	params       AssembleParams
	registry     *adapters.Registry
	exchange     *adapters.Exchange
	recorder     *audit.Recorder
	declarations adapters.DeclarationRepository
	submissions  *orchestration.Submissions
}

// assembleAgentProxy 装配 8788 的五个端口。
//
// 五个端口分属三层：认证在 core/agentauth，服务反查与 Lease 匹配在本包，
// 执行在 adapter。proxy.New 在任一为空时拒绝构造 —— 装不出一个「认证器为空」
// 的代理，这条性质比这里的任何一行代码都重要。
func assembleAgentProxy(shared assembleFaceParams) (*faceServer, error) {
	agentProxy, err := proxy.New(proxy.Options{
		Availability:  shared.availability,
		Authenticator: proxyAuthenticator{agentIdentifier{agents: shared.services.AgentAuth}},
		Services: &proxyServices{
			declarations: shared.declarations, registry: shared.registry,
		},
		Leases:   &proxyLeases{leases: shared.services.Leases},
		Exchange: &proxyExchange{exchange: shared.exchange},
		Audits:   &proxyAudits{recorder: shared.recorder},
		Logger:   shared.params.Logger,
	})
	if err != nil {
		return nil, err
	}

	return newFaceServer(faceOptions{
		Name: "agent-proxy",
		Address: net.JoinHostPort(shared.params.Config.ListenAddress,
			strconv.Itoa(shared.params.Config.AgentProxyPort)),
		Handler: proxy.NewHandler(agentProxy),
		Logger:  shared.params.Logger,
	}), nil
}

// assembleMCP 装配 8789。
//
// 工具清单在这里生成，生成失败即启动失败：清单只来自能力声明，
// 一份不合规的声明不该退化成「少一个工具」（REQ-MCP-001 AC1）。
func assembleMCP(shared assembleFaceParams) (*faceServer, error) {
	catalog, err := mcpsrv.NewCatalog(shared.registry)
	if err != nil {
		return nil, err
	}
	calls, err := newMCPCalls(mcpCalls{
		submissions: shared.submissions,
		requests:    shared.services.Requests,
		leases:      shared.services.Leases, registry: shared.registry,
		exchange: shared.exchange, recorder: shared.recorder,
		clock: shared.params.Clock, ids: shared.services.IDs,
	})
	if err != nil {
		return nil, err
	}

	server, err := mcpsrv.NewServer(mcpsrv.Options{
		Availability:  shared.availability,
		Catalog:       catalog,
		Authenticator: mcpAuthenticator{agentIdentifier{agents: shared.services.AgentAuth}},
		Calls:         calls,
		Logger:        shared.params.Logger,
	})
	if err != nil {
		return nil, err
	}

	return newFaceServer(faceOptions{
		Name: "mcp",
		Address: net.JoinHostPort(shared.params.Config.ListenAddress,
			strconv.Itoa(shared.params.Config.MCPPort)),
		Handler: mcpsrv.NewHTTPHandler(server),
		Logger:  shared.params.Logger,
	}), nil
}

// assembleAdapters 注册编译期就在的 Adapter（ADR-009：不动态加载）。
//
// generichttp 不在其中：它的定义由用户在运行期给出，没有定义就没有 Base URL，
// 也就没有可注册的东西。
func assembleAdapters() (*adapters.Registry, error) {
	gitHub, err := github.New(github.Options{})
	if err != nil {
		return nil, err
	}
	cloudFlare, err := cloudflare.New(cloudflare.Options{})
	if err != nil {
		return nil, err
	}
	openAI, err := model.New(model.Options{Provider: model.ProviderOpenAI})
	if err != nil {
		return nil, err
	}
	anthropic, err := model.New(model.Options{Provider: model.ProviderAnthropic})
	if err != nil {
		return nil, err
	}
	return adapters.New(gitHub, cloudFlare, openAI, anthropic)
}

// assembleServices 装配 8787 面需要的全部 core 组件。
//
// 顺序是自下而上的：仓储没有依赖，Manager 依赖仓储，Pipeline 依赖 Manager。
// 写成一串顺序调用而不是拆成若干个构造函数，是因为这里的价值恰恰在于
// 「一眼看得出谁依赖谁」——拆开之后那张图就散在几个文件里了。
func assembleServices(
	database *store.DB, ids *ulid.Generator, params AssembleParams,
) (httpapi.Services, error) {
	requests := repo.NewCapabilityRequests(database)
	decisions := repo.NewDecisions(database)
	identities := repo.NewIdentities(database)
	approvals := repo.NewApprovals(database)
	leases := repo.NewLeases(database)
	memories := repo.NewTrustMemories(database)
	events := repo.NewAuditEvents(database)
	agents := repo.NewAgents(database)

	recorder, err := audit.NewRecorder(events, params.Clock, ids)
	if err != nil {
		return httpapi.Services{}, err
	}

	approvalManager, err := approval.NewManager(approval.Options{
		Approvals: approvals, Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}
	leaseManager, err := lease.NewManager(lease.Options{
		Leases: leases, Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}
	memoryManager, err := trust.NewManager(trust.Options{
		Memories: memories, Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	intents, err := intent.NewResolver(intent.Options{})
	if err != nil {
		return httpapi.Services{}, err
	}
	scopes, err := scope.NewResolver(params.Clock)
	if err != nil {
		return httpapi.Services{}, err
	}

	agentAuth, err := agentauth.NewService(agentauth.Options{
		Agents: agents, Devices: repo.NewDevices(database), Workspaces: repo.NewWorkspaces(database),
		Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	orchestration, err := pipeline.New(pipeline.Options{
		Requests: requests, Decisions: decisions, Identities: identities, Agents: agentAuth,
		Approvals: approvalManager, Leases: leaseManager, Memories: memoryManager,
		Intents: intents, Scopes: scopes, Audit: recorder,
		Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	preferences, err := settings.NewStore(settings.Options{
		Settings: repo.NewSettings(database), Clock: params.Clock, IDs: ids,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	credentialRegistry, err := credentials.New(credentials.Options{
		Providers:  repo.NewCredentialProviders(database),
		References: repo.NewCredentialReferences(database),
		Leases:     leases,
		Clock:      params.Clock,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	// 保险库在这里装配，但**不读文件也不判断它存不存在**（localvault.New 的契约）：
	// 那个判断只发生在解锁时，且不对外区分（REQ-CRED-004 AC1）。没有它，
	// 高风险审批在 Console 上永远只能被拒绝（用户决定 D-14 方案 C）。
	vault, err := localvault.New(localvault.Options{
		Path:  filepath.Join(params.Config.DataDir(), localvault.FileName),
		Clock: params.Clock,
	})
	if err != nil {
		return httpapi.Services{}, err
	}

	return httpapi.Services{
		Pipeline: orchestration, Requests: requests, Decisions: decisions,
		Approvals: approvalManager, Leases: leaseManager, Memories: memoryManager,
		Identities: identities, Credentials: credentialRegistry, Ledger: events,
		Agents: agents, AgentAuth: agentAuth, Preferences: preferences, Vault: vault,
		Config: params.Config, Clock: params.Clock, IDs: ids,
	}, nil
}

// Close 关闭装配出来的资源。
//
// 先停止服务再关数据库：反过来的话，正在处理的请求会在写账本时撞上一个
// 已经关掉的连接池，而账本写失败按 ADR-004 要让请求失败 —— 那是一次
// 本可以避免的、由关机顺序制造出来的失败。
func (g *Gateway) Close() error {
	g.Availability.Stop()

	var closing []error
	if g.events != nil {
		g.events.Close()
	}
	if g.db != nil {
		closing = append(closing, g.db.Close())
	}
	return errors.Join(closing...)
}

func databasePath(settings config.Config) string {
	return filepath.Join(settings.DataDir(), databaseFileName)
}
