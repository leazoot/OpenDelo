package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * MCP 工具调用编排的用例（REQ-MCP-003）。
 *
 * 这是产品里第一条走完全程的路，因此用例守的是**全程的性质**，不是某一步的返回值：
 *
 *   放行时：凭据到了外部服务、没有到答复里、Lease 被计了一次、账本上有执行记录。
 *   不放行时：一个字节都没有出站。
 *
 * 「没有出站」这条只有假上游能证明。它记下每一次到达，用例断言它一次都没被叫过 ——
 * 断言编排的返回值证明不了这件事：一个先发请求再拒绝的实现照样返回拒绝。
 */

const (
	// gitHubReadTool 与 mcpsrv 从编译期 Adapter 生成的工具名一致。
	gitHubReadTool  = "github.repository.read"
	gitHubWriteTool = "github.issue.create"
	callOperationID = "operation_mcp_call_test"
)

// declaredCapabilities 是数据库里那份声明。
//
// 它与编译期 Adapter 描述的是同一批操作，但来源不同：清单来自编译期注册表，
// 决策用的映射表来自这份存下来的声明。用例里两份都写出来，是因为真实部署里
// 它们本来就是两份 —— 对不上时该拒绝，而不是让某一份去将就另一份。
const declaredCapabilities = `[` +
	`{"tool":"github.repository.read","operation":"read_repository","method":"GET",` +
	`"path":"/repos/{owner}/{repo}","risk":"low","idempotent":true,"reversible":true,` +
	`"sensitive_data":false,"resource_keys":["owner","repo"]},` +
	`{"tool":"github.issue.create","operation":"create_issue","method":"POST",` +
	`"path":"/repos/{owner}/{repo}/issues","risk":"medium","idempotent":false,` +
	`"reversible":true,"sensitive_data":false,"resource_keys":["owner","repo"]}` +
	`]`

// arrivals 是假 GitHub，记下每一次到达。
type arrivals struct {
	server        *httptest.Server
	count         int
	authorization string
	// body 是出站正文。**必须记**：只记次数与 Authorization 的话，
	// 「请求发出去了、带着凭据」与「请求发出去了、带着凭据、内容却是空的」
	// 长成同一个样子 —— 后者正是 2026-08-04 人工验收撞出的缺陷。
	body []byte
	// status 是假上游要答的状态码，0 表示 200。用例据此模拟真实的失败形状：
	// 「连不上」与「上游答了 422」在账本上不该长成同一件事。
	status int
}

func newArrivals(t *testing.T, body string) *arrivals {
	t.Helper()

	recorded := &arrivals{}
	recorded.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.count++
		recorded.authorization = r.Header.Get("Authorization")
		sent, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("假上游读请求正文失败：%v", readErr)
		}
		recorded.body = sent
		w.Header().Set("Content-Type", "application/json")
		if recorded.status != 0 {
			w.WriteHeader(recorded.status)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("假上游写响应失败：%v", err)
		}
	}))
	t.Cleanup(recorded.server.Close)
	return recorded
}

// sentinelSource 是凭据来源的 fake，取回的永远是哨兵。
type sentinelSource struct {
	value secret.Value
}

func (s *sentinelSource) Fetch(context.Context, string) (secret.Value, error) { return s.value, nil }

type callHarness struct {
	calls    *mcpCalls
	upstream *arrivals
	requests *repo.CapabilityRequests
	leases   *lease.Manager
	events   *repo.AuditEvents
	moment   *clock.Fixed
	database *store.DB
}

// newCallHarness 装出一条真的决策链路：真数据库、真 Pipeline、真 Adapter。
//
// core 内部不互相 mock：只有外部服务与凭据来源
// 是 fake。决策顺序、Scope 收敛、Lease 计量在这里都是真的在跑。
func newCallHarness(t *testing.T, environment matcher.Environment) callHarness {
	t.Helper()

	database := fixtures.SeededRequestChain(t)
	upstream := newArrivals(t, `{"id":1,"name":"opendelo","full_name":"runcoor/opendelo",`+
		`"private":false,"default_branch":"main","token":"`+sentinel.SentinelToken+`"}`)

	seedDeclaration(t, database, upstream.server.URL)
	setIdentityEnvironment(t, database, environment)

	moment := clock.NewFixed(time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
	ids := ulid.New(moment)

	adapter, err := github.New(github.Options{BaseURL: upstream.server.URL})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	registry, err := adapters.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}
	credential := secret.New([]byte(sentinel.SentinelToken))
	t.Cleanup(credential.Zero)
	identities := repo.NewIdentities(database)
	exchange, err := adapters.NewExchange(registry, &sentinelSource{value: credential},
		&credentialReferences{identities: identities})
	if err != nil {
		t.Fatalf("构造 Exchange 失败：%v", err)
	}

	events := repo.NewAuditEvents(database)
	recorder, err := audit.NewRecorder(events, moment, ids)
	if err != nil {
		t.Fatalf("构造账本失败：%v", err)
	}
	requests := repo.NewCapabilityRequests(database)
	leases := newLeaseManager(t, database, moment, ids)

	decide, err := orchestration.New(orchestration.Submissions{
		Pipeline:   newPipeline(t, database, recorder, leases, moment, ids),
		Identities: identities, Agents: repo.NewAgents(database),
		Devices: repo.NewDevices(database), Declarations: repo.NewServiceAdapters(database),
		Registry: registry, Previews: exchange, Requests: requests,
		Arrivals: announcerFor(t, registry, moment),
		Clock:    moment, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造请求编排失败：%v", err)
	}

	calls, err := newMCPCalls(mcpCalls{
		submissions: decide, requests: requests, leases: leases, registry: registry,
		exchange: exchange, recorder: recorder, clock: moment, ids: ids,
	})
	if err != nil {
		t.Fatalf("构造 MCP 编排失败：%v", err)
	}

	return callHarness{
		calls: calls, upstream: upstream, requests: requests,
		leases: leases, events: events, moment: moment, database: database,
	}
}

func seedDeclaration(t *testing.T, database *store.DB, baseURL string) {
	t.Helper()

	declaration := fixtures.Declaration(
		fixtures.WithDeclarationCapabilities(declaredCapabilities))
	declaration.BaseURL = baseURL
	if _, err := repo.NewServiceAdapters(database).CreateDeclaration(t.Context(), declaration); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}
}

// setIdentityEnvironment 换掉夹具身份的环境标记。
//
// 环境是风险因子之一：同一个只读操作在生产与非生产下的结论可以不同，
// 而用例要能分别走到这两条路上。
func setIdentityEnvironment(t *testing.T, database *store.DB, environment matcher.Environment) {
	t.Helper()

	if environment == matcher.EnvironmentProduction {
		return
	}
	if _, err := database.Writer().ExecContext(t.Context(),
		`UPDATE identities SET environment = ? WHERE id = ?`,
		string(environment), fixtures.DefaultIdentityID); err != nil {
		t.Fatalf("更新身份环境失败：%v", err)
	}
}

func newLeaseManager(
	t *testing.T, database *store.DB, moment clock.Clock, ids *ulid.Generator,
) *lease.Manager {
	t.Helper()

	manager, err := lease.NewManager(lease.Options{
		Leases: repo.NewLeases(database), Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	return manager
}

func newPipeline(
	t *testing.T, database *store.DB, recorder *audit.Recorder,
	leases *lease.Manager, moment clock.Clock, ids *ulid.Generator,
) *pipeline.Pipeline {
	t.Helper()

	approvals, err := approval.NewManager(approval.Options{
		Approvals: repo.NewApprovals(database), Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造审批 Manager 失败：%v", err)
	}
	memories, err := trust.NewManager(trust.Options{
		Memories: repo.NewTrustMemories(database), Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造记忆 Manager 失败：%v", err)
	}
	intents, err := intent.NewResolver(intent.Options{})
	if err != nil {
		t.Fatalf("构造意图解析器失败：%v", err)
	}
	scopes, err := scope.NewResolver(moment)
	if err != nil {
		t.Fatalf("构造 Scope 收敛器失败：%v", err)
	}

	sessions, err := agentauth.NewService(agentauth.Options{
		Agents: repo.NewAgents(database), Devices: repo.NewDevices(database),
		Workspaces: repo.NewWorkspaces(database), Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Agent 会话服务失败：%v", err)
	}

	orchestration, err := pipeline.New(pipeline.Options{
		Requests: repo.NewCapabilityRequests(database), Decisions: repo.NewDecisions(database),
		Identities: repo.NewIdentities(database), Agents: sessions, Approvals: approvals,
		Leases: leases, Memories: memories, Intents: intents, Scopes: scopes,
		Audit: recorder, Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造决策链路失败：%v", err)
	}
	return orchestration
}

func callContext(t *testing.T) context.Context {
	t.Helper()
	return logging.WithOperationID(t.Context(), callOperationID)
}

func readCall(arguments string) mcpsrv.Call {
	return mcpsrv.Call{
		Caller: mcpsrv.Caller{
			AgentID: fixtures.DefaultAgentID, WorkspaceID: fixtures.DefaultWorkspaceID,
		},
		Tool: gitHubReadTool, Service: "github", Operation: "read_repository",
		Arguments: json.RawMessage(arguments),
	}
}

const readArguments = `{"owner":"runcoor","repo":"opendelo"}`

func TestMCPCall_AutoAllowedRead_ExecutesAndNeverReturnsTheCredential(t *testing.T) {
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	outcome, err := harness.calls.Call(ctx, readCall(readArguments))
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if outcome.Refused {
		t.Fatalf("低风险只读被拒了：%s", outcome.Text)
	}

	// 一、凭据到了外部服务。
	if harness.upstream.count != 1 {
		t.Fatalf("外部服务被请求了 %d 次，期望 1 次", harness.upstream.count)
	}
	if !strings.Contains(harness.upstream.authorization, sentinel.SentinelToken) {
		t.Errorf("出站请求没有带上凭据：%q", harness.upstream.authorization)
	}

	// 二、凭据没有到答复里 —— 包括外部服务在响应体里回显的那一次
	//（REQ-NFR-002 AC1 的「MCP 响应」那一面）。
	if strings.Contains(outcome.Text, sentinel.SentinelToken) {
		t.Errorf("答复里出现了凭据哨兵：%s", outcome.Text)
	}
	if !strings.Contains(outcome.Text, "opendelo") {
		t.Errorf("答复里没有任何有用内容：%s", outcome.Text)
	}

	// 三、请求走到了终态，账本上有一条执行记录。
	assertOnlyRequestStatus(t, harness, pipeline.StatusSucceeded)
	leaseID := assertExecutionRecorded(t, harness, audit.OutcomeSucceeded)

	// 四、Lease 被计了一次。不计量的话次数上限形同虚设。
	//
	// 按主键读而不是在活跃列表里找：默认次数上限用满之后 Lease 立刻转为
	// exhausted 并离开活跃列表（REQ-LEASE-001 AC3），在那里找等于找不到。
	used, err := harness.leases.ByID(t.Context(), leaseID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if used.UsedRequests != 1 {
		t.Errorf("Lease 的使用次数为 %d，期望 1", used.UsedRequests)
	}
}

func TestMCPCall_RequiresApproval_ProducesNoOutboundTraffic(t *testing.T) {
	// 中风险写操作、没有学过 —— 结论是等人确认。此时最要紧的性质是
	// 「一个字节都没出去」（成功标准 S10）。
	harness := newCallHarness(t, matcher.EnvironmentProduction)
	ctx := callContext(t)

	outcome, err := harness.calls.Call(ctx, mcpsrv.Call{
		Caller: mcpsrv.Caller{
			AgentID: fixtures.DefaultAgentID, WorkspaceID: fixtures.DefaultWorkspaceID,
		},
		Tool: gitHubWriteTool, Service: "github", Operation: "create_issue",
		Arguments: json.RawMessage(`{"owner":"runcoor","repo":"opendelo","title":"修一个空指针"}`),
	})
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}

	if !outcome.Refused {
		t.Fatal("需要人工确认的写操作被直接执行了")
	}
	if harness.upstream.count != 0 {
		t.Errorf("外部服务被请求了 %d 次，期望 0 次", harness.upstream.count)
	}
	assertOnlyRequestStatus(t, harness, pipeline.StatusAwaitingApproval)
}

func TestMCPCall_ToolMissingFromTheStoredDeclaration_IsRefusedAndAudited(t *testing.T) {
	// 编译期注册表有这个操作、数据库里的声明没有。两份对不上时的答案只能是拒绝，
	// 而且这次拒绝必须留在账本上（REQ-AUDIT-001 AC1：无未审计路径）。
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	call := readCall(`{"owner":"runcoor","repo":"opendelo","number":"7"}`)
	call.Tool = "github.pull_request.read"
	call.Operation = "read_pull_request"

	outcome, err := harness.calls.Call(ctx, call)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	if !outcome.Refused {
		t.Fatal("声明里没有的工具被执行了")
	}
	if harness.upstream.count != 0 {
		t.Errorf("外部服务被请求了 %d 次，期望 0 次", harness.upstream.count)
	}

	assertOnlyRequestStatus(t, harness, pipeline.StatusDenied)
	if !hasEventType(t, harness, audit.EventError) {
		t.Error("账本里没有这次拒绝的记录")
	}
}

func TestMCPCall_MalformedArguments_AreRefusedBeforeAnythingIsWritten(t *testing.T) {
	// 参数不是 JSON 对象属于输入校验，不进入决策流程也不写业务表
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	_, err := harness.calls.Call(ctx, readCall(`"not an object"`))
	if !apperr.Is(err, apperr.CodeInvalidRequest) {
		t.Fatalf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
	}

	// 夹具本身有一条请求，因此期望的是「没有多出来」。
	written, listErr := harness.requests.RequestsByStatus(ctx, pipeline.StatusReceived, 10)
	if listErr != nil {
		t.Fatalf("读取能力请求失败：%v", listErr)
	}
	if len(written) != 1 {
		t.Errorf("库里有 %d 条 received 请求，期望仍是夹具那一条", len(written))
	}
	if harness.upstream.count != 0 {
		t.Errorf("外部服务被请求了 %d 次，期望 0 次", harness.upstream.count)
	}
}

func TestMCPCall_UpstreamFailure_IsRecordedAsAFailedExecutionNotASuccess(t *testing.T) {
	// 外部服务没答成时，账本上必须是 failed。记成 succeeded 的话，
	// 事后翻账本会看到一次「成功」的调用，而它其实什么都没做成。
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	// 把假上游关掉，模拟外部服务不可达。
	harness.upstream.server.Close()

	outcome, err := harness.calls.Call(ctx, readCall(readArguments))
	if err != nil {
		t.Fatalf("上游不可达被折成了错误而不是一次失败的执行：%v", err)
	}
	// 不是「被拒」：这次调用得到了授权、也执行了，只是外部服务没答成。
	// 把两者混为一谈会让模型以为再试一次也没用。
	if outcome.Refused {
		t.Error("上游失败被报成了策略拒绝")
	}

	assertOnlyRequestStatus(t, harness, pipeline.StatusFailed)
	assertExecutionRecorded(t, harness, audit.OutcomeFailed)
}

/*
 * TestMCPCall_UpstreamStatus_ReachesTheLedger（回归，R-44）
 *
 * 上游的状态码在映射成对外错误码的那一刻被丢掉了，账本里记下的是我们自己的
 * **502**。于是排查一次失败只能看见「网关不可用」，而真正发生的是 GitHub 答了
 * 422 —— 人工验收时为定位这一件事多花了半小时。
 *
 * 状态码不是报文：对外的错误码与消息一个字不改，只是账本上记下的那个数字
 * 变成真实发生过的那一个（`audit.Event.ResponseStatus` 的本意正是如此，
 * 它的注释写着「为 0 表示没有发出过外部请求」）。
 */
func TestMCPCall_UpstreamStatus_ReachesTheLedger(t *testing.T) {
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	harness.upstream.status = http.StatusUnprocessableEntity

	if _, err := harness.calls.Call(ctx, readCall(readArguments)); err != nil {
		t.Fatalf("上游答 422 被折成了错误而不是一次失败的执行：%v", err)
	}

	event := executionEvent(t, harness)
	if event.ResponseStatus != http.StatusUnprocessableEntity {
		t.Errorf("账本上的 response_status 是 %d，上游真正答的是 422 —— "+
			"记我们自己的映射码，排查时看见的就只有「网关不可用」", event.ResponseStatus)
	}
}

// TestMCPCall_NoOutboundResponse_LeavesTheStatusAtZero：连不上时**没有**上游状态码。
//
// 补这一条是因为「记真实状态码」很容易滑成「记点什么」：把连不上也记成某个
// 数字的话，`ResponseStatus` 那句「为 0 表示没有发出过外部请求」就不再成立。
func TestMCPCall_NoOutboundResponse_LeavesTheStatusAtZero(t *testing.T) {
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	harness.upstream.server.Close()

	if _, err := harness.calls.Call(ctx, readCall(readArguments)); err != nil {
		t.Fatalf("上游不可达被折成了错误：%v", err)
	}

	if event := executionEvent(t, harness); event.ResponseStatus != 0 {
		t.Errorf("上游根本没答，账本上却记了 %d", event.ResponseStatus)
	}
}

// TestMCPCall_SuccessfulCall_RecordsWhatTheUpstreamAnswered：成功那一路同样记真的。
func TestMCPCall_SuccessfulCall_RecordsWhatTheUpstreamAnswered(t *testing.T) {
	harness := newCallHarness(t, matcher.EnvironmentNonProduction)
	ctx := callContext(t)

	harness.upstream.status = http.StatusCreated

	if _, err := harness.calls.Call(ctx, readCall(readArguments)); err != nil {
		t.Fatalf("调用失败：%v", err)
	}

	if event := executionEvent(t, harness); event.ResponseStatus != http.StatusCreated {
		t.Errorf("上游答的是 201，账本上记的是 %d", event.ResponseStatus)
	}
}

// executionEvent 取出这次调用留下的那条执行记录。
func executionEvent(t *testing.T, harness callHarness) audit.Event {
	t.Helper()

	events, err := harness.events.Events(t.Context(), time.Time{}, 20)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	for _, event := range events {
		if event.Type == audit.EventAdapterExecuted {
			return event
		}
	}
	t.Fatal("账本里没有执行记录")
	return audit.Event{}
}

func TestNewMCPCalls_MissingDependency_IsRefused(t *testing.T) {
	// 少一项依赖时不构造。缺账本的编排会一路跑到放行才发现写不了审计，
	// 而那时凭据已经取出来过一次。
	if _, err := newMCPCalls(mcpCalls{}); !apperr.Is(err, apperr.CodeInternal) {
		t.Fatalf("依赖全空却构造成功了：%v", err)
	}
}

// assertOnlyRequestStatus 断言这次调用产生的那条请求停在预期状态。
func assertOnlyRequestStatus(t *testing.T, harness callHarness, want pipeline.RequestStatus) {
	t.Helper()

	written, err := harness.requests.RequestsByStatus(t.Context(), want, 10)
	if err != nil {
		t.Fatalf("读取能力请求失败：%v", err)
	}
	for _, request := range written {
		if request.ID != fixtures.DefaultRequestID {
			return
		}
	}
	t.Errorf("没有一条本次调用产生的请求处于 %s", want)
}

// assertExecutionRecorded 断言账本上有一条结果符合预期的执行记录，并返回它指向的 Lease。
func assertExecutionRecorded(t *testing.T, harness callHarness, want audit.Outcome) string {
	t.Helper()

	events, err := harness.events.Events(t.Context(), time.Time{}, 20)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	for _, event := range events {
		if event.Type != audit.EventAdapterExecuted {
			continue
		}
		if event.Outcome != want {
			t.Fatalf("执行记录的结果为 %s，期望 %s", event.Outcome, want)
		}
		if event.OperationID != callOperationID {
			t.Errorf("执行记录的 operation_id 为 %q，期望 %q", event.OperationID, callOperationID)
		}
		if event.LeaseID == "" {
			t.Error("执行记录没有指向任何一条 Lease")
		}
		return event.LeaseID
	}
	t.Fatalf("账本里没有执行记录（期望结果 %s）", want)
	return ""
}

func hasEventType(t *testing.T, harness callHarness, want audit.EventType) bool {
	t.Helper()

	events, err := harness.events.Events(t.Context(), time.Time{}, 20)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}

// announcerFor 装出这些用例要的到达通知。
//
// 用真实实现而不是桩：这些用例跑的是完整的决策路径，而通知就在那条路径上 ——
// 换成桩之后，「通知会不会把请求带失败」这件事在这里就测不到了。
func announcerFor(t *testing.T, registry *adapters.Registry, moment clock.Clock) *httpapi.Announcer {
	t.Helper()

	quiet := slog.New(slog.NewJSONHandler(io.Discard, nil))
	announcer, err := httpapi.NewAnnouncer(httpapi.Announcement{
		Events: httpapi.NewBroker(quiet), Capabilities: registry, Clock: moment, Logger: quiet,
	})
	if err != nil {
		t.Fatalf("构造到达通知失败：%v", err)
	}
	return announcer
}
