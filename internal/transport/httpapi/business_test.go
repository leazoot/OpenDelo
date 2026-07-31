package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * REQ-API-001 前 11 个端点的契约测试。
 *
 * 端点背后接的是真实的 core 组件与临时 SQLite（见 newBackend），
 * 不 mock 任何 core 包 —— 否则测的是替身之间的接线。
 */

// api 是一次用例用的服务：一个 backend 加上装在它上面的处理器。
type api struct {
	backend backend
	handler http.Handler
}

// newAPI 装一个 Console 调用方的服务。
func newAPI(t *testing.T) api {
	t.Helper()
	return newAPIFor(t, httpapi.Caller{})
}

// newAPIFor 装一个指定调用方的服务。
//
// 走 NewBusinessHandler 而不是整台 Server：8787 面只认会话令牌，
// Agent 的调用方身份由 MCP 与 Proxy 两个面填入，那两个面在 S5.2 才有。
// 这里直接给出调用方，测的正是这些处理器在拿到 Agent 身份时的行为。
func newAPIFor(t *testing.T, caller httpapi.Caller) api {
	t.Helper()
	return newAPIForBackend(t, newBackend(t), caller)
}

// newAPIWith 在一个自己装配好的 backend 上建服务。
func newAPIWith(t *testing.T, all backend, caller httpapi.Caller) api {
	t.Helper()
	return newAPIForBackend(t, all, caller)
}

// newAPIForBackend 在一个已有的 backend 上再装一个别的调用方，
// 用来测「同一份数据，换一个调用方看到什么」。
func newAPIForBackend(t *testing.T, all backend, caller httpapi.Caller) api {
	t.Helper()

	handler, err := httpapi.NewBusinessHandler(
		all.Services, logging.New(logging.Options{Writer: io.Discard}))
	if err != nil {
		t.Fatalf("构造业务处理器失败：%v", err)
	}

	return api{backend: all, handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(httpapi.WithCaller(r.Context(), caller)))
		})}
}

func (a api) call(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequestWithContext(t.Context(), method, target, reader)
	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeInto(t *testing.T, response *httptest.ResponseRecorder, into any) {
	t.Helper()

	if err := json.Unmarshal(response.Body.Bytes(), into); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，正文为 %q", err, response.Body.String())
	}
}

// ——— 路径与方法与 PRD §27 完全一致 ———

func TestRoutes_MatchTheEndpointListInThePRD(t *testing.T) {
	// REQ-API-001 的 20 个 + REQ-API-002 的 10 个（gateway/status 由 status.go
	// 单独注册），路径与方法逐条核对。多一个或改一个方法都会失败。
	expected := []httpapi.Route{
		{Method: http.MethodPost, Pattern: "/v1/capability-requests"},
		{Method: http.MethodGet, Pattern: "/v1/capability-requests/{id}"},
		{Method: http.MethodPost, Pattern: "/v1/capability-requests/{id}/cancel"},
		{Method: http.MethodGet, Pattern: "/v1/approvals"},
		{Method: http.MethodPost, Pattern: "/v1/approvals/{id}/allow-once"},
		{Method: http.MethodPost, Pattern: "/v1/approvals/{id}/allow-task"},
		{Method: http.MethodPost, Pattern: "/v1/approvals/{id}/allow-project"},
		// PRD §13.2 的第五种操作。§27 的端点清单漏了它，ActionAlwaysAsk 因此
		// 一直无路可走（用户决定 D-15 ③ 补上）。
		{Method: http.MethodPost, Pattern: "/v1/approvals/{id}/always-ask"},
		{Method: http.MethodPost, Pattern: "/v1/approvals/{id}/deny"},
		{Method: http.MethodGet, Pattern: "/v1/leases"},
		{Method: http.MethodPost, Pattern: "/v1/leases/{id}/shorten"},
		{Method: http.MethodDelete, Pattern: "/v1/leases/{id}"},

		{Method: http.MethodGet, Pattern: "/v1/identities"},
		{Method: http.MethodPost, Pattern: "/v1/identities/connect"},
		{Method: http.MethodPost, Pattern: "/v1/identities/{id}/verify"},
		{Method: http.MethodDelete, Pattern: "/v1/identities/{id}"},

		{Method: http.MethodGet, Pattern: "/v1/trust-memories"},
		{Method: http.MethodPatch, Pattern: "/v1/trust-memories/{id}"},
		{Method: http.MethodDelete, Pattern: "/v1/trust-memories/{id}"},

		{Method: http.MethodGet, Pattern: "/v1/audit-events"},
		// REQ-API-002 的导出端点。它是 PRD §27 之外唯一多出来的一条，
		// 服务 REQ-AUDIT-004，不引入新业务能力。
		{Method: http.MethodGet, Pattern: "/v1/audit-events/export"},
		{Method: http.MethodGet, Pattern: "/v1/audit-events/{id}"},

		// REQ-API-002 的其余端点。gateway/status 不在这张表里：
		// 它由 status.go 单独注册，audit-events/export 已在上面。
		{Method: http.MethodGet, Pattern: "/v1/agents"},
		// 注册与断开只服务 opendelo run，是 REQ-CLI-002 AC3 的前提
		// （用户决定 D-13 扩充 REQ-API-002）。
		{Method: http.MethodPost, Pattern: "/v1/agents/register"},
		{Method: http.MethodPost, Pattern: "/v1/agents/{id}/trust"},
		{Method: http.MethodPost, Pattern: "/v1/agents/{id}/disconnect"},
		{Method: http.MethodGet, Pattern: "/v1/preferences"},
		{Method: http.MethodPatch, Pattern: "/v1/preferences"},
		// 建立保险库由用户决定 D-15 扩充 REQ-API-002：没有它，
		// 保险库文件不存在，强认证在真实安装上无从谈起。
		{Method: http.MethodPost, Pattern: "/v1/vault"},
		{Method: http.MethodPost, Pattern: "/v1/vault/unlock"},
		{Method: http.MethodPost, Pattern: "/v1/vault/lock"},
		{Method: http.MethodGet, Pattern: "/v1/events"},
	}

	routes := httpapi.Routes()
	if len(routes) != len(expected) {
		t.Fatalf("注册了 %d 个端点，期望 %d 个：%v",
			len(routes), len(expected), routes)
	}
	for index, want := range expected {
		if routes[index] != want {
			t.Errorf("第 %d 个端点是 %v，期望 %v", index+1, routes[index], want)
		}
	}
}

func TestRoutes_WrongMethodReturnsTheErrorEnvelopeNotPlainText(t *testing.T) {
	// REQ-API-003：405 也要是 JSON 错误体。ServeMux 自带的 405 是裸文本。
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/capability-requests", "")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码为 %d，期望 405", response.Code)
	}
	if allow := response.Header().Get("Allow"); allow != "POST" {
		t.Errorf("Allow 为 %q，期望 POST", allow)
	}
	if code := decodeErrorCode(t, response); code != "invalid_request" {
		t.Errorf("错误码为 %q", code)
	}
}

func TestStatusForCode_CoversEveryRegisteredErrorCode(t *testing.T) {
	// 新增一个错误码却忘了给它状态码时，它会静默变成 500，
	// 「这次为什么失败」就在网络层丢失了。
	for _, code := range apperr.All() {
		if _, known := httpapi.StatusForCode(code); !known {
			t.Errorf("错误码 %s 没有对应的 HTTP 状态码", code)
		}
	}
}

// ——— POST /v1/capability-requests（REQ-CAP-001）———

const submitBody = `{"agent_id":"` + fixtures.DefaultAgentID + `",` +
	`"workspace":"` + fixtures.DefaultWorkspaceID + `",` +
	`"service":"github","operation":"pull_request.create",` +
	`"resource":{"repo":"Runcoor/opendelo"},` +
	`"desired_change":{"title":"修一个空指针"},` +
	`"reason":"用户要求提交这次修复"}`

func TestSubmit_DrivesTheDecisionPipelineInsteadOfStoppingAtReceived(t *testing.T) {
	// 原用例断言的是 `received`，那不是正确行为：请求落库就返回 202，调用方拿到
	// 一个永远不会有结论的编号，也没有别的办法推动它往下走。现在提交即进入决策
	// 链路，`received` 只是状态机的起点。
	//
	// 这里的结论是 `denied`：夹具的编译期注册表不声明 `pull_request.create`，
	// 而未声明的能力一律拒绝（Fail Closed）。要紧的是**它已经有结论了**。
	all := newAPI(t)

	response := all.call(t, http.MethodPost, "/v1/capability-requests", submitBody)
	if response.Code != http.StatusAccepted {
		t.Fatalf("状态码为 %d，期望 202，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if view.Status == string(pipeline.StatusReceived) {
		t.Error("请求停在 received —— 决策链路没有被驱动")
	}
	if view.Status != string(pipeline.StatusDenied) {
		t.Errorf("状态为 %q，期望 denied", view.Status)
	}
	// 拒绝的**理由**要对得上：数据库声明认得这个操作，编译期注册表不认得，
	// 因此这是一次由阻断产生的 fail_closed，链路跑完并留下了决策记录。
	// 只断言 denied 是不够的 —— 别的原因（Scope 收敛不了、身份对不上）
	// 同样得到 denied，那样这条用例就与「未声明的能力被拒」无关了。
	written, err := all.backend.Events.Events(t.Context(), time.Time{}, 10)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	if len(written) == 0 {
		t.Fatal("这次拒绝没有留在账本上")
	}
	if !strings.Contains(written[0].Metadata, "capability_not_offered") {
		t.Errorf("拒绝理由为 %q，期望 capability_not_offered", written[0].Metadata)
	}
	if view.OperationID == "" {
		t.Error("响应缺少 operation_id，用户无法在账本中定位这次请求")
	}

	stored, err := all.backend.Requests.RequestByID(t.Context(), view.ID)
	if err != nil {
		t.Fatalf("请求没有落库：%v", err)
	}
	if stored.Operation != "pull_request.create" {
		t.Errorf("落库的操作是 %q", stored.Operation)
	}
}

// declaredRead 是一份既被编译期 Adapter 提供、又被数据库声明认得的能力。
//
// 两份来源必须对上操作名才走得到结论：清单来自编译期注册表，决策用的映射表
// 来自存下来的声明。夹具默认的 `pull_request.create` 只存在于后者。
const declaredRead = `[{"tool":"github.repository.read","operation":"read_repository",` +
	`"method":"GET","path":"/repos/{owner}/{repo}","risk":"low","idempotent":true,` +
	`"reversible":true,"sensitive_data":false,"resource_keys":["owner","repo"]}]`

const readBody = `{"agent_id":"` + fixtures.DefaultAgentID + `",` +
	`"workspace":"` + fixtures.DefaultWorkspaceID + `",` +
	`"service":"github","operation":"read_repository",` +
	`"resource":{"owner":"Runcoor","repo":"opendelo"},` +
	`"reason":"读一下仓库信息"}`

func TestSubmit_ADeclaredOperation_ComesBackWithARealDecision(t *testing.T) {
	// 请求不只是「有了结论」，而是**那条决策链路真的跑过** ——
	// 响应里带着风险等级、匹配到的身份与理由，它们只可能来自 pipeline.Handle。
	all := newAPI(t)

	declaration := fixtures.Declaration(fixtures.WithDeclarationCapabilities(declaredRead))
	if _, err := repo.NewServiceAdapters(all.backend.DB).CreateDeclaration(
		t.Context(), declaration); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	response := all.call(t, http.MethodPost, "/v1/capability-requests", readBody)
	if response.Code != http.StatusAccepted {
		t.Fatalf("状态码为 %d，期望 202，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if view.Decision == nil {
		t.Fatal("响应里没有决策 —— 请求没有走完链路")
	}
	if view.Decision.RiskLevel == "" || view.Decision.ReasonCode == "" {
		t.Errorf("决策缺少风险等级或理由：%+v", view.Decision)
	}
	if view.Decision.IdentityID != fixtures.DefaultIdentityID {
		t.Errorf("决策匹配到的身份为 %q，期望 %q",
			view.Decision.IdentityID, fixtures.DefaultIdentityID)
	}
	if view.Status == string(pipeline.StatusReceived) {
		t.Error("请求停在 received")
	}
}

func TestSubmit_DeclaredInTheDatabaseButNotOfferedByAnyAdapter_IsRefused(t *testing.T) {
	// 两份来源不一致的那一种：数据库里的声明认得这个操作，编译期注册表不认得。
	// 意图解析因此照常成功 —— 挡住它的只有那条显式的阻断。没有它，这次请求会
	// 一路走到放行，签出一条**没有任何 Adapter 执行得了**的 Lease。
	all := newAPI(t)

	if _, err := repo.NewServiceAdapters(all.backend.DB).CreateDeclaration(
		t.Context(), fixtures.Declaration()); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	response := all.call(t, http.MethodPost, "/v1/capability-requests", submitBody)
	if response.Code != http.StatusAccepted {
		t.Fatalf("状态码为 %d，期望 202，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if view.Status != string(pipeline.StatusDenied) {
		t.Fatalf("状态为 %q，期望 denied", view.Status)
	}
	// 决策记录必须存在：这次拒绝是决策引擎给出的，不是链路在装配输入之前就断了。
	if view.Decision == nil {
		t.Fatal("拒绝没有留下决策记录")
	}
	if view.Decision.ReasonCode != "fail_closed" {
		t.Errorf("拒绝理由为 %q，期望 fail_closed", view.Decision.ReasonCode)
	}
}

func TestSubmit_MissingRequiredField_Returns400AndWritesNothing(t *testing.T) {
	// REQ-CAP-001 AC1：错误体包含缺失字段名，且 capability_requests 表无新增记录。
	cases := map[string]string{
		"agent_id":  `{"workspace":"w","service":"s","operation":"o","resource":{},"reason":"r"}`,
		"workspace": `{"agent_id":"a","service":"s","operation":"o","resource":{},"reason":"r"}`,
		"service":   `{"agent_id":"a","workspace":"w","operation":"o","resource":{},"reason":"r"}`,
		"operation": `{"agent_id":"a","workspace":"w","service":"s","resource":{},"reason":"r"}`,
		"resource":  `{"agent_id":"a","workspace":"w","service":"s","operation":"o","reason":"r"}`,
		"reason":    `{"agent_id":"a","workspace":"w","service":"s","operation":"o","resource":{}}`,
	}

	for field, body := range cases {
		t.Run("缺少 "+field, func(t *testing.T) {
			all := newAPI(t)
			before := countRequests(t, all)

			response := all.call(t, http.MethodPost, "/v1/capability-requests", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
			if code := decodeErrorCode(t, response); code != "invalid_request" {
				t.Errorf("错误码为 %q", code)
			}
			if fields := errorFields(t, response); !slices.Contains(fields, field) {
				t.Errorf("错误体里的 fields 为 %v，没有出现缺失的字段名 %s", fields, field)
			}
			if after := countRequests(t, all); after != before {
				t.Errorf("校验失败的请求仍然落了库：%d → %d", before, after)
			}
		})
	}
}

func TestSubmit_RequestingACredentialIsRefusedAndAudited(t *testing.T) {
	// REQ-CAP-001 AC2：operation=credential.read 被拒并产生一条审计事件。
	for _, operation := range []string{
		"credential.read", "secret.get", "token.export", "vault.dump", "CREDENTIAL.READ",
	} {
		t.Run(operation, func(t *testing.T) {
			all := newAPI(t)
			before := countRequests(t, all)

			body := `{"agent_id":"` + fixtures.DefaultAgentID + `",` +
				`"workspace":"` + fixtures.DefaultWorkspaceID + `",` +
				`"service":"github","operation":"` + operation + `",` +
				`"resource":{"repo":"x"},"reason":"r"}`
			response := all.call(t, http.MethodPost, "/v1/capability-requests", body)

			if response.Code != http.StatusForbidden {
				t.Fatalf("状态码为 %d，期望 403", response.Code)
			}
			if code := decodeErrorCode(t, response); code != "capability_not_offered" {
				t.Errorf("错误码为 %q，期望 capability_not_offered", code)
			}
			if after := countRequests(t, all); after != before {
				t.Error("索取凭据的请求落了库")
			}
			assertAuditEvent(t, all, "security.secret_request_blocked")
		})
	}
}

func TestSubmit_UnknownFieldIsRefused(t *testing.T) {
	// REQ-SCOPE-002 的更强形式：请求里带 scope 会被拒，而不是被悄悄忽略 ——
	// 忽略掉的话调用方会以为自己指定的范围生效了。
	all := newAPI(t)

	body := `{"agent_id":"a","workspace":"w","service":"s","operation":"o",` +
		`"resource":{},"reason":"r","scope":{"expires_in":"999h"}}`
	response := all.call(t, http.MethodPost, "/v1/capability-requests", body)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestSubmit_AgentCannotSubmitUnderAnotherAgentsName(t *testing.T) {
	all := newAPIFor(t, httpapi.Caller{AgentID: "agent-other"})

	response := all.call(t, http.MethodPost, "/v1/capability-requests", submitBody)
	if response.Code != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.Code)
	}
}

// ——— GET / cancel（REQ-CAP-002）———

func TestShow_ReturnsStatusAndDecisionWithoutAnyCredentialField(t *testing.T) {
	// AC1：返回状态与决策结果，且不含 Header、Cookie、Token。
	all := newAPI(t)

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	// withheld_operations 单独拿开再扫。它列的是**没有**被授予的操作名，
	// 而 GitHub 的能力表里就有一个叫 update_secret —— 「这次放行不包含修改
	// 仓库 Secret」正是用户最需要看见的那句否定句。为了让关键词扫描过关而
	// 把它藏起来，等于用一个词表换掉一条需求（REQ-APPROVAL-001 AC4）。
	// 它的取值另有约束：只能来自编译期的能力声明，见下面那个用例。
	body := strings.ToLower(bodyWithoutWithheldOperations(t, response.Body.Bytes()))
	for _, banned := range []string{"authorization", "cookie", "token", "secret", "password"} {
		if strings.Contains(body, banned) {
			t.Errorf("响应里出现了 %q", banned)
		}
	}
}

// bodyWithoutWithheldOperations 去掉 withheld_operations 之后重新序列化。
//
// 删字段而不是从词表里删词：响应里**别的**地方再出现 secret 仍然要失败。
func bodyWithoutWithheldOperations(t *testing.T, raw []byte) string {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("响应不是 JSON 对象：%v", err)
	}
	if _, present := decoded["withheld_operations"]; !present {
		t.Fatal("响应里没有 withheld_operations")
	}
	delete(decoded, "withheld_operations")

	rest, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("重新序列化失败：%v", err)
	}
	return string(rest)
}

func TestShow_WithheldOperations_ComeFromTheCapabilityTableNotFromACannedSentence(t *testing.T) {
	// REQ-APPROVAL-001 AC4：说清这次放行之外还有什么没给。三句写死的否定句
	// 说的都是真的，只是不完整 —— 少列的正是「这个服务还有哪些操作没给」。
	all := newAPI(t)

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if len(view.WithheldOperations) == 0 {
		t.Fatal("没有列出任何仍然关闭的操作")
	}
	for _, operation := range view.WithheldOperations {
		if operation == view.Operation {
			t.Errorf("被授予的 %q 出现在了仍然关闭的清单里", operation)
		}
	}
	// 清单来自能力声明：夹具请求的服务是 github，因此里面必须有 github
	// 声明过、而这次没给的那些操作。写死一份清单会让这条断言与实现同源，
	// 所以只钉住几个必然在场的。
	for _, expected := range []string{"delete_repository", "update_collaborator"} {
		if !slices.Contains(view.WithheldOperations, expected) {
			t.Errorf("仍然关闭的清单里没有 %q：%v", expected, view.WithheldOperations)
		}
	}
}

func TestSubmit_TheGrantedOperationIsNotAmongTheWithheldOnes(t *testing.T) {
	// 上一条用例里被授予的操作压根不在能力表内，因此「清单里没有它」是白给的。
	// 这里提交一个**两边都认得**的操作，减法才真的被执行到。
	all := newAPI(t)

	declaration := fixtures.Declaration(fixtures.WithDeclarationCapabilities(declaredRead))
	if _, err := repo.NewServiceAdapters(all.backend.DB).CreateDeclaration(
		t.Context(), declaration); err != nil {
		t.Fatalf("写入 Adapter 声明失败：%v", err)
	}

	response := all.call(t, http.MethodPost, "/v1/capability-requests", readBody)
	if response.Code != http.StatusAccepted {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if slices.Contains(view.WithheldOperations, "read_repository") {
		t.Errorf("这次授予的 read_repository 出现在了仍然关闭的清单里：%v", view.WithheldOperations)
	}
	if !slices.Contains(view.WithheldOperations, "read_issue") {
		t.Errorf("同一服务下没给的 read_issue 不在清单里：%v", view.WithheldOperations)
	}
}

func TestShow_NeverPreviewed_SaysSoRatherThanInventingAnOldValue(t *testing.T) {
	// 没查过就是没查过。编一栏出来，用户会以为自己看到的是外部服务里的现值。
	all := newAPI(t)

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	// 线上的形状是 JSON null。断言这一个而不是「切片为空」：空数组会被
	// 界面读成「查过，没有可对照的字段」，那是另一句话。
	if got := string(view.ChangePreview); got != "null" {
		t.Errorf("没有查勘过却给出了旧值：%s", got)
	}
}

func TestShow_PreviewedRequest_CarriesTheOldValueToTheFolio(t *testing.T) {
	all := newAPI(t)

	const preview = `[{"resource":"opendelo","field":"title","before":"旧标题","after":"新标题"}]`
	advance(t, all, pipeline.StatusReceived, pipeline.StatusAwaitingApproval)
	if err := all.backend.Requests.SaveChangePreview(t.Context(), fixtures.DefaultRequestID,
		preview, pipeline.StatusAwaitingApproval, fixtures.Instant); err != nil {
		t.Fatalf("写入查勘结果失败：%v", err)
	}

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)

	var changes []struct {
		Field  string `json:"field"`
		Before string `json:"before"`
	}
	if err := json.Unmarshal(view.ChangePreview, &changes); err != nil {
		t.Fatalf("旧值不是变化数组：%v（%s）", err, view.ChangePreview)
	}
	if len(changes) != 1 || changes[0].Before != "旧标题" {
		t.Errorf("旧值为 %+v，期望带上查勘查到的那一个", changes)
	}
}

func TestShow_AnotherAgentsRequestIsNotFoundNotForbidden(t *testing.T) {
	// REQ-CAP-002 AC2：Agent A 查询 Agent B 的请求返回 404 而不是 403 ——
	// 403 等于承认这个 id 存在。
	all := newAPIFor(t, httpapi.Caller{AgentID: "agent-b"})

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
	if code := decodeErrorCode(t, response); code != "not_found" {
		t.Errorf("错误码为 %q，期望 not_found", code)
	}

	// 与一个真的不存在的 id 给出完全相同的答复，否则「存在」这件事仍然漏了出去。
	missing := all.call(t, http.MethodGet, "/v1/capability-requests/01J0000000000000000MISSING", "")
	if missing.Code != response.Code {
		t.Errorf("不存在的 id 返回 %d，别人的 id 返回 %d，两者必须一致",
			missing.Code, response.Code)
	}
	if errorDetail(t, missing) != errorDetail(t, response) {
		t.Error("两种情况的错误信息不同，存在性仍然可以被区分出来")
	}
}

func TestShow_OwnRequestIsVisibleToItsOwnAgent(t *testing.T) {
	all := newAPIFor(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	response := all.call(t, http.MethodGet,
		"/v1/capability-requests/"+fixtures.DefaultRequestID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("Agent 看不到自己发起的请求：%d", response.Code)
	}
}

func TestCancel_OnARequestThatIsNotWaiting_Returns409(t *testing.T) {
	// REQ-CAP-002 AC3：取消已完成的请求返回 409，状态不变。
	all := newAPI(t)
	advance(t, all, pipeline.StatusReceived, pipeline.StatusDenied)

	response := all.call(t, http.MethodPost,
		"/v1/capability-requests/"+fixtures.DefaultRequestID+"/cancel", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409", response.Code)
	}

	still, err := all.backend.Requests.RequestByID(t.Context(), fixtures.DefaultRequestID)
	if err != nil {
		t.Fatalf("读取请求失败：%v", err)
	}
	if still.Status != pipeline.StatusDenied {
		t.Errorf("状态被改成了 %s", still.Status)
	}
}

func TestCancel_OnAWaitingRequest_MovesItToCancelled(t *testing.T) {
	all := newAPI(t)
	advance(t, all, pipeline.StatusReceived, pipeline.StatusAwaitingApproval)

	response := all.call(t, http.MethodPost,
		"/v1/capability-requests/"+fixtures.DefaultRequestID+"/cancel", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.CapabilityRequestView
	decodeInto(t, response, &view)
	if view.Status != string(pipeline.StatusCancelled) {
		t.Errorf("状态为 %q，期望 cancelled", view.Status)
	}
}

func TestCancel_AnotherAgentsRequestIsNotFound(t *testing.T) {
	all := newAPIFor(t, httpapi.Caller{AgentID: "agent-b"})
	advanceWith(t, all.backend, pipeline.StatusReceived, pipeline.StatusAwaitingApproval)

	response := all.call(t, http.MethodPost,
		"/v1/capability-requests/"+fixtures.DefaultRequestID+"/cancel", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}

// ——— 辅助 ———

func countRequests(t *testing.T, all api) int {
	t.Helper()

	var total int
	for _, status := range []pipeline.RequestStatus{
		pipeline.StatusReceived, pipeline.StatusAwaitingApproval,
		pipeline.StatusDenied, pipeline.StatusAutoAllowed,
	} {
		found, err := all.backend.Requests.RequestsByStatus(t.Context(), status, 100)
		if err != nil {
			t.Fatalf("列出请求失败：%v", err)
		}
		total += len(found)
	}
	return total
}

// errorFields 取出校验失败时同级的 fields 数组。
func errorFields(t *testing.T, response *httptest.ResponseRecorder) []string {
	t.Helper()

	var envelope struct {
		Fields []string `json:"fields"`
	}
	decodeInto(t, response, &envelope)
	return envelope.Fields
}

func errorDetail(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeInto(t, response, &envelope)
	return envelope.Error.Code + "|" + envelope.Error.Message
}

func assertAuditEvent(t *testing.T, all api, wanted string) {
	t.Helper()

	events, err := all.backend.Events.Events(t.Context(), zeroTime, 100)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	for _, event := range events {
		if string(event.Type) == wanted {
			return
		}
	}
	t.Errorf("账本里没有 %s 事件", wanted)
}

func advance(t *testing.T, all api, from, to pipeline.RequestStatus) {
	t.Helper()
	advanceWith(t, all.backend, from, to)
}

func advanceWith(t *testing.T, all backend, from, to pipeline.RequestStatus) {
	t.Helper()

	if _, err := all.Requests.AdvanceRequest(
		t.Context(), fixtures.DefaultRequestID, from, to, fixtures.Instant); err != nil {
		t.Fatalf("推进请求状态失败：%v", err)
	}
}

// approvalOf 走真实的决策链路产出一个待审批项，返回它的 id 与风险等级。
func approvalOf(t *testing.T, all api) approval.Approval {
	t.Helper()

	pending, err := all.backend.Services.Approvals.Pending(t.Context(), 10)
	if err != nil {
		t.Fatalf("列出待审批失败：%v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("待审批有 %d 条，期望 1 条", len(pending))
	}
	return pending[0]
}

func TestNewBusinessHandler_MissingAnyService_IsRefused(t *testing.T) {
	// 端点少一个依赖就起不来：一台「能起但一按就 500」的 Gateway 比起不来更糟。
	full := newBackend(t).Services
	logger := logging.New(logging.Options{Writer: io.Discard})

	for name, blank := range map[string]func(*httpapi.Services){
		"Pipeline":  func(s *httpapi.Services) { s.Pipeline = nil },
		"Requests":  func(s *httpapi.Services) { s.Requests = nil },
		"Decisions": func(s *httpapi.Services) { s.Decisions = nil },
		"Approvals": func(s *httpapi.Services) { s.Approvals = nil },
		"Leases":    func(s *httpapi.Services) { s.Leases = nil },
		"Clock":     func(s *httpapi.Services) { s.Clock = nil },
		"IDs":       func(s *httpapi.Services) { s.IDs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			services := full
			blank(&services)
			if _, err := httpapi.NewBusinessHandler(services, logger); err == nil {
				t.Errorf("%s 为空时仍然构造出了处理器", name)
			}
		})
	}

	if _, err := httpapi.NewBusinessHandler(full, nil); err == nil {
		t.Error("没有日志器却构造出了处理器")
	}
}

func TestShow_ARequestWithoutADesiredChangeReportsNullNotAnEmptyObject(t *testing.T) {
	// 「这次请求没有变更」与「变更是个空对象」在审批页面上是两句不同的话。
	all := newAPI(t)

	readOnly := `{"agent_id":"` + fixtures.DefaultAgentID + `",` +
		`"workspace":"` + fixtures.DefaultWorkspaceID + `",` +
		`"service":"github","operation":"pull_request.list",` +
		`"resource":{"repo":"Runcoor/opendelo"},"reason":"看一眼"}`
	created := all.call(t, http.MethodPost, "/v1/capability-requests", readOnly)
	if created.Code != http.StatusAccepted {
		t.Fatalf("提交失败：%d %s", created.Code, created.Body.String())
	}
	var view httpapi.CapabilityRequestView
	decodeInto(t, created, &view)

	response := all.call(t, http.MethodGet, "/v1/capability-requests/"+view.ID, "")
	// 解成 map 才能区分「字段是 null」与「字段根本没出现」：
	// 前者是要表达的事实，后者会让界面无从判断。
	var raw map[string]json.RawMessage
	decodeInto(t, response, &raw)

	change, present := raw["desired_change"]
	if !present {
		t.Fatal("响应里没有 desired_change 字段")
	}
	if string(change) != "null" {
		t.Errorf("desired_change 为 %s，读操作应当是 null", change)
	}
	if string(raw["resource"]) == "null" {
		t.Error("resource 不该是 null，这次请求带着资源")
	}
}

// discardLogger 是一个丢弃全部输出的日志器，供不关心日志的用例使用。
func discardLogger() *slog.Logger {
	return logging.New(logging.Options{Writer: io.Discard})
}

// waitingOn 在一个已有的 backend 上产出一个待审批项，返回它的 id。
func waitingOn(t *testing.T, all backend) string {
	t.Helper()
	return waiting(t, newAPIForBackend(t, all, httpapi.Caller{})).ID
}
