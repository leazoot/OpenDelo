package fixtures

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/credential/localvault"
	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Web API 端点背后那一整套真实组件。
 *
 * 集中在这里，避免契约用例与哨兵用例各造一份「差不多的 Gateway」。
 * 里面没有任何替身：core 包之间不得互相 mock，
 * 否则测的是接线而不是这条链路。
 */

// Gateway 是装配好的一套服务，外加用例断言时要用的仓储。
type Gateway struct {
	Services   httpapi.Services
	DB         *store.DB
	Clock      *clock.Fixed
	Requests   *repo.CapabilityRequests
	Decisions  *repo.Decisions
	Approvals  *repo.Approvals
	Leases     *repo.Leases
	Events     *repo.AuditEvents
	Identities *repo.Identities
	Memories   *repo.TrustMemories
	Agents     *repo.Agents
	Settings   *repo.Settings
	// Declarations 让用例能直接看库里有哪些服务声明 —— 「连接身份时写了声明吗」
	// 只能这样问，端点的响应里没有这一项。
	Declarations *repo.ServiceAdapters
}

// credentialsOf 装配凭据注册表。
//
// 不登记任何来源：这些用例不取明文，只读引用元数据。真的要探测时，
// 注册表会因为找不到对应种类的来源而拒绝 —— 那正是 Fail Closed 的表现。
func credentialsOf(
	t testing.TB, db *store.DB, fixed *clock.Fixed, leases *repo.Leases,
) *credentials.Registry {
	t.Helper()

	registry, err := credentials.New(credentials.Options{
		Providers:  repo.NewCredentialProviders(db),
		References: repo.NewCredentialReferences(db),
		Leases:     leases,
		Clock:      fixed,
	})
	if err != nil {
		t.Fatalf("构造凭据注册表失败：%v", err)
	}
	return registry
}

// agentService 装配 Agent 认证服务，只为让「确认信任」这个端点能走通。
func agentService(
	t testing.TB, db *store.DB, fixed *clock.Fixed, ids *ulid.Generator,
) *agentauth.Service {
	t.Helper()

	service, err := agentauth.NewService(agentauth.Options{
		Agents:     repo.NewAgents(db),
		Devices:    repo.NewDevices(db),
		Workspaces: repo.NewWorkspaces(db),
		Clock:      fixed,
		IDs:        ids,
	})
	if err != nil {
		t.Fatalf("构造 Agent 认证服务失败：%v", err)
	}
	return service
}

func preferenceStore(
	t testing.TB, db *store.DB, fixed *clock.Fixed, ids *ulid.Generator,
) *settings.Store {
	t.Helper()

	store, err := settings.NewStore(settings.Options{
		Settings: repo.NewSettings(db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造偏好 Store 失败：%v", err)
	}
	return store
}

// NewGateway 在一个已写好前置行的临时数据库上装配全部业务组件。
func NewGateway(t testing.TB) Gateway {
	t.Helper()

	db := SeededRequestChain(t)
	fixed := clock.NewFixed(Instant)
	ids := ulid.New(fixed)

	events := repo.NewAuditEvents(db)
	recorder, err := audit.NewRecorder(events, fixed, ids)
	if err != nil {
		t.Fatalf("构造审计写入器失败：%v", err)
	}

	approvals := repo.NewApprovals(db)
	approvalManager, err := approval.NewManager(approval.Options{
		Approvals: approvals, Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}

	leases := repo.NewLeases(db)
	leaseManager, err := lease.NewManager(lease.Options{Leases: leases, Clock: fixed, IDs: ids})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}

	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(db), Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}
	intents, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造意图解析器失败：%v", err)
	}
	scopes, err := scope.NewResolver(fixed)
	if err != nil {
		t.Fatalf("构造 Scope 收敛器失败：%v", err)
	}

	identities := repo.NewIdentities(db)
	agents := repo.NewAgents(db)
	requests := repo.NewCapabilityRequests(db)
	decisions := repo.NewDecisions(db)
	sessions := agentService(t, db, fixed, ids)
	line, err := pipeline.New(pipeline.Options{
		Requests: requests, Decisions: decisions, Identities: identities,
		Agents:    sessions,
		Approvals: approvalManager, Leases: leaseManager, Memories: memories,
		Intents: intents, Scopes: scopes, Audit: recorder,
		Clock: fixed, IDs: ids, Mode: decision.ModeBalanced,
	})
	if err != nil {
		t.Fatalf("构造 Pipeline 失败：%v", err)
	}

	credentialRegistry := credentialsOf(t, db, fixed, leases)
	decide, adapterRegistry, stream := submissionsOf(t, db, line, credentialRegistry, fixed)
	declarer, err := orchestration.NewDeclarer(orchestration.DeclarerOptions{
		Registry: adapterRegistry, Declarations: repo.NewServiceAdapters(db),
		Clock: fixed, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造服务声明器失败：%v", err)
	}

	return Gateway{
		Services: httpapi.Services{
			Pipeline: line, Submissions: decide, Capabilities: adapterRegistry,
			Requests: requests, Decisions: decisions,
			Approvals: approvalManager, Leases: leaseManager, Memories: memories,
			Identities: identities, Credentials: credentialRegistry, Declarer: declarer,
			Ledger: events, Agents: agents, AgentAuth: sessions,
			Preferences: preferenceStore(t, db, fixed, ids), Config: config.Default(),
			Events: stream,
			Clock:  fixed, IDs: ids,
		},
		DB: db, Clock: fixed, Requests: requests, Decisions: decisions,
		Approvals: approvals, Leases: leases, Events: events,
		Identities: identities, Memories: repo.NewTrustMemories(db),
		Agents: agents, Settings: repo.NewSettings(db),
		Declarations: repo.NewServiceAdapters(db),
	}
}

// healthySource 是一个永远可用的凭据来源。
//
// 只为让「验证身份」这条路径能走到成功那一支：真实来源要装 1Password CLI
// 或碰用户钥匙串，而测试规则禁止用例那样做。
// Fetch 返回哨兵值 —— 任何一处把它带进响应或日志，哨兵扫描都会命中。
type healthySource struct{}

func (healthySource) Kind() credentials.ProviderKind { return credentials.KindOnePassword }

func (healthySource) Available(context.Context) error { return nil }

func (healthySource) Fetch(
	context.Context, credentials.Reference,
) (secret.Value, error) {
	return secret.New([]byte(sentinel.SentinelToken)), nil
}

// NewGatewayWithHealthySource 与 NewGateway 相同，但注册表里有一个永远可用的来源。
func NewGatewayWithHealthySource(t testing.TB) Gateway {
	t.Helper()

	gateway := NewGateway(t)
	if err := gateway.Services.Credentials.Register(healthySource{}); err != nil {
		t.Fatalf("登记凭据来源失败：%v", err)
	}
	return gateway
}

// downSource 是一个登记过、但此刻取不到凭据的来源。
//
// 对应真实世界里 `op` 装着却没登录、钥匙串锁着这两种情况：来源在注册表里，
// Available 却不通。它与「来源根本没登记」是两条不同的路径，都必须走向拒绝。
type downSource struct{}

func (downSource) Kind() credentials.ProviderKind { return credentials.KindOnePassword }

func (downSource) Available(context.Context) error {
	return apperr.New(apperr.CodeProviderUnavailable).WithDetail("来源在用例里被设为不可用")
}

func (downSource) Fetch(
	context.Context, credentials.Reference,
) (secret.Value, error) {
	return secret.Value{}, apperr.New(apperr.CodeProviderUnavailable).
		WithDetail("来源在用例里被设为不可用")
}

// NewGatewayWithDownSource 与 NewGateway 相同，但注册表里那个来源探不通。
func NewGatewayWithDownSource(t testing.TB) Gateway {
	t.Helper()

	gateway := NewGateway(t)
	if err := gateway.Services.Credentials.Register(downSource{}); err != nil {
		t.Fatalf("登记凭据来源失败：%v", err)
	}
	return gateway
}

// VaultMasterPassword 是用例用的主密码。它是一个哨兵值：
// 任何一处把它带进响应或日志，哨兵扫描都会命中。
const VaultMasterPassword = sentinel.SentinelPassword

// NewGatewayWithVault 与 NewGateway 相同，另外建好一个已解锁的本地保险库。
//
// 保险库文件在 t.TempDir() 里，用例之间完全隔离，绝不碰用户真实数据目录
func NewGatewayWithVault(t testing.TB) Gateway {
	t.Helper()

	gateway := NewGateway(t)
	vault, err := localvault.New(localvault.Options{
		Path:  filepath.Join(t.TempDir(), "vault.json"),
		Clock: gateway.Clock,
	})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}
	if err = vault.CreateWith([]byte(VaultMasterPassword)); err != nil {
		t.Fatalf("建立保险库失败：%v", err)
	}

	gateway.Services.Vault = vault
	return gateway
}

// NewGatewayWithEmptyVault 装配一个**配置了保险库但文件尚不存在**的 Gateway。
//
// 这正是全新安装的处境：REQ-CRED-004 §2 的主密码还没设过。
func NewGatewayWithEmptyVault(t testing.TB) Gateway {
	t.Helper()

	gateway := NewGateway(t)
	vault, err := localvault.New(localvault.Options{
		Path:  filepath.Join(t.TempDir(), localvault.FileName),
		Clock: gateway.Clock,
	})
	if err != nil {
		t.Fatalf("构造保险库失败：%v", err)
	}
	gateway.Services.Vault = vault
	return gateway
}

// submissionsOf 装出接入面共用的那段决策输入装配。
//
// 用真实实现而不是桩：`POST /v1/capability-requests` 的用例要证明的正是
// 「请求落库之后确实进入了决策链路」，而一个桩证明不了这件事。
func submissionsOf(
	t testing.TB, db *store.DB, line *pipeline.Pipeline,
	creds *credentials.Registry, fixed *clock.Fixed,
) (*orchestration.Submissions, *adapters.Registry, *httpapi.Broker) {
	t.Helper()

	gitHub, err := github.New(github.Options{})
	if err != nil {
		t.Fatalf("构造 GitHub Adapter 失败：%v", err)
	}
	registry, err := adapters.New(gitHub)
	if err != nil {
		t.Fatalf("构造 Adapter 注册表失败：%v", err)
	}
	// 用真实 Exchange 而不是桩：查勘与执行共用同一段凭据处理，桩会让
	// 「查勘也会去取凭据」这件事在用例里消失。GitHub Adapter 不实现查勘，
	// 因此这些用例走的是「这个服务没有旧值可查」那一支。
	exchange, err := adapters.NewExchange(registry, creds, identityReferences{
		identities: repo.NewIdentities(db),
	})
	if err != nil {
		t.Fatalf("构造 Exchange 失败：%v", err)
	}

	// 用真实的到达通知而不是桩：「缝前来了人，开着的 Console 上看得见吗」
	// 只有真的播一次事件才答得上来。
	quiet := slog.New(slog.NewJSONHandler(io.Discard, nil))
	events := httpapi.NewBroker(quiet)
	announcer, err := httpapi.NewAnnouncer(httpapi.Announcement{
		Events: events, Capabilities: registry, Clock: fixed, Logger: quiet,
	})
	if err != nil {
		t.Fatalf("构造到达通知失败：%v", err)
	}

	decide, err := orchestration.New(orchestration.Submissions{
		Pipeline: line, Identities: repo.NewIdentities(db),
		Agents: repo.NewAgents(db), Devices: repo.NewDevices(db),
		Declarations: repo.NewServiceAdapters(db), Registry: registry,
		Previews: exchange, Requests: repo.NewCapabilityRequests(db),
		Arrivals: announcer,
		Clock:    fixed, Logger: quiet,
	})
	if err != nil {
		t.Fatalf("构造请求编排失败：%v", err)
	}
	return decide, registry, events
}

// identityReferences 回答「这个身份用的是哪条凭据引用」。
//
// 与组装根里的那一份同形。写在这里而不是导出组装根的版本：那个包是 cli，
// 让 fixtures 依赖它会把整棵命令行子树拖进每一个契约用例。
type identityReferences struct {
	identities *repo.Identities
}

func (r identityReferences) ReferenceFor(ctx context.Context, identityID string) (string, error) {
	identity, err := r.identities.IdentityByID(ctx, identityID)
	if err != nil {
		return "", err
	}
	if identity.CredentialReferenceID == "" {
		return "", apperr.New(apperr.CodeProviderUnavailable).
			WithDetail("身份 " + identityID + " 还没有绑定凭据引用")
	}
	return identity.CredentialReferenceID, nil
}
