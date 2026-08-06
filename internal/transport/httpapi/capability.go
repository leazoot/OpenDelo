package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * Agent API：能力请求的提交、查询与取消（PRD §27、REQ-CAP-001/002）。
 */

// submitRequest 是 POST /v1/capability-requests 的请求体（REQ-CAP-001）。
//
// 这里没有 scope、permissions、credential 之类的字段：它们**不可提交**，
// 而不是「提交了会被忽略」。DisallowUnknownFields 让带上它们的请求直接 400，
// 调用方因此不会误以为自己指定的范围生效了（REQ-SCOPE-002 的更强形式）。
type submitRequest struct {
	AgentID       string          `json:"agent_id"`
	Workspace     string          `json:"workspace"`
	Service       string          `json:"service"`
	Operation     string          `json:"operation"`
	Resource      json.RawMessage `json:"resource"`
	DesiredChange json.RawMessage `json:"desired_change"`
	Reason        string          `json:"reason"`
}

// credentialOperations 是直接索取凭据的操作名（REQ-CAP-001 行为要求 2）。
//
// 按前缀匹配而不是全等：credential.read 与 credential.export 是同一类请求，
// 而这一类里的每一个都要被拒。
var credentialOperations = []string{"credential.", "secret.", "token.", "vault."}

// missingField 报告哪个必填字段没给，全部齐备时返回空串。
//
// 顺序固定：错误信息里出现的字段名因此是确定的，用例能逐条断言（REQ-CAP-001 AC1）。
func (s submitRequest) missingField() string {
	fields := []struct {
		name  string
		blank bool
	}{
		{"agent_id", strings.TrimSpace(s.AgentID) == ""},
		{"workspace", strings.TrimSpace(s.Workspace) == ""},
		{"service", strings.TrimSpace(s.Service) == ""},
		{"operation", strings.TrimSpace(s.Operation) == ""},
		{"resource", len(s.Resource) == 0},
		{"reason", strings.TrimSpace(s.Reason) == ""},
	}
	for _, field := range fields {
		if field.blank {
			return field.name
		}
	}
	return ""
}

// submit 接收一次能力请求。
//
// 校验不过就**不写库**（REQ-CAP-001 AC1）：一条字段都填不全的请求落了库，
// 账本上就多出一条永远不会有结论的记录。
func (e *endpoints) submit(w http.ResponseWriter, r *http.Request) {
	var body submitRequest
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if missing := body.missingField(); missing != "" {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("缺少必填字段 "+missing), missing)
		return
	}
	if err := checkResourceObject(body.Resource, "resource"); err != nil {
		e.fail(w, r, err)
		return
	}
	if err := checkResourceObject(body.DesiredChange, "desired_change"); err != nil {
		e.fail(w, r, err)
		return
	}

	caller := callerFrom(r.Context())
	if caller.IsAgent() && caller.AgentID != body.AgentID {
		// 冒用别人的 agent_id 不是「查不到」而是「不许」：调用方已经通过认证，
		// 它知道自己是谁，这条路径不存在可以泄漏的存在性。
		e.fail(w, r, apperr.New(apperr.CodeForbidden).
			WithDetail("不能以别的 Agent 的名义提交请求"))
		return
	}

	if err := e.refuseCredentialRequest(w, r, body); err != nil {
		return
	}

	id, err := e.services.IDs.NewID()
	if err != nil {
		e.fail(w, r, err)
		return
	}
	now := e.services.Clock.Now()

	created, err := e.services.Requests.CreateRequest(r.Context(), pipeline.CapabilityRequest{
		ID:            id,
		OperationID:   logging.OperationIDFrom(r.Context()),
		AgentID:       body.AgentID,
		WorkspaceID:   body.Workspace,
		Service:       body.Service,
		Operation:     body.Operation,
		Resource:      string(body.Resource),
		DesiredChange: string(body.DesiredChange),
		Reason:        body.Reason,
		Status:        pipeline.StatusReceived,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		e.fail(w, r, err)
		return
	}

	// 落库之后立刻进入决策链路（REQ-CAP-001 的状态机）。**不返回一条停在
	// `received` 的请求** —— 那样调用方拿到 202 却永远等不到结论，而它没有
	// 别的办法能推动这次请求往下走。
	result, err := e.services.Submissions.Decide(r.Context(), created)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	record := decidedView(result)
	withheld := withheldOperations(e.services.Capabilities, result.Request.Service, result.Request.Operation)
	// arrival 事件由决策路径自己播（internal/orchestration 的到达通知）：
	// 三个接入面共用那一条，在这里再播一次只会让缝上多出一行重复的。
	// 事件流只到 Console，那一侧要看得见强认证；写给调用方的那一份不带它。
	if caller.IsAgent() {
		hideStrongAuth(record)
	}
	writeJSON(w, r, e.logger, http.StatusAccepted, requestView(result.Request, record, withheld))
}

// decidedView 取出这次决策对外的样子。
//
// Fail Closed 那条路径上没有决策记录（链路在装配输入之前就拒绝了），此时返回 nil：
// 请求的 `status` 已经是 `denied`，编一个空壳决策出来只会让界面显示一条
// 没有风险因子、没有身份、没有理由的记录。
func decidedView(result pipeline.Result) *DecisionView {
	if result.Decision.ID == "" {
		return nil
	}
	view := decisionView(result.Decision)
	return &view
}

// refuseCredentialRequest 拦下直接索取凭据的请求（REQ-CAP-001 AC2）。
//
// 拒绝之前先记审计：这类请求是安全事件，账本上必须留下痕迹，
// 而账本写不进去时整件事都要失败（ADR-004）。写出响应后返回非 nil，
// 调用方据此停止处理。
func (e *endpoints) refuseCredentialRequest(
	w http.ResponseWriter, r *http.Request, body submitRequest,
) error {
	operation := strings.ToLower(strings.TrimSpace(body.Operation))
	requested := false
	for _, prefix := range credentialOperations {
		if strings.HasPrefix(operation, prefix) {
			requested = true
			break
		}
	}
	if !requested {
		return nil
	}

	refusal := apperr.New(apperr.CodeCapabilityNotOffered).
		WithDetail("请求描述的是凭据而不是能力：" + body.Operation)
	if err := e.services.Pipeline.RecordSecretRequestBlocked(r.Context(), pipeline.BlockedRequest{
		OperationID: logging.OperationIDFrom(r.Context()),
		AgentID:     body.AgentID,
		WorkspaceID: body.Workspace,
		Service:     body.Service,
		Operation:   body.Operation,
	}); err != nil {
		e.fail(w, r, err)
		return err
	}

	e.fail(w, r, refusal)
	return refusal
}

// checkResourceObject 要求 resource 与 desired_change 是 JSON 对象。
//
// 数组或裸标量会让 Scope 里的资源维度取不出来，而取不出来的维度按
// Fail Closed 只能拒绝 —— 与其让它走到决策链路里被拒，不如在入口说清原因。
func checkResourceObject(raw json.RawMessage, field string) error {
	if len(raw) == 0 {
		return nil
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("字段 " + field + " 必须是 JSON 对象")
	}
	return nil
}

// show 返回一次请求的状态与决策（REQ-CAP-002 AC1）。
func (e *endpoints) show(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	request, err := e.visibleRequest(r, id)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	record, err := e.decisionFor(r.Context(), request.ID)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	if callerFrom(r.Context()).IsAgent() {
		hideStrongAuth(record)
	}
	writeJSON(w, r, e.logger, http.StatusOK, requestView(request, record,
		withheldOperations(e.services.Capabilities, request.Service, request.Operation)))
}

// cancel 撤回一次仍在等待人工确认的请求（REQ-CAP-002）。
//
// 只有 awaiting_approval 能被撤回：已经执行完的请求撤不回来，
// 状态机的条件更新会返回冲突（AC3）。
func (e *endpoints) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	request, err := e.visibleRequest(r, id)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	cancelled, err := e.services.Requests.AdvanceRequest(r.Context(), request.ID,
		pipeline.StatusAwaitingApproval, pipeline.StatusCancelled, e.services.Clock.Now())
	if err != nil {
		e.fail(w, r, err)
		return
	}

	record, err := e.decisionFor(r.Context(), cancelled.ID)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	view := requestView(cancelled, record,
		withheldOperations(e.services.Capabilities, cancelled.Service, cancelled.Operation))
	e.publish(r, EventPassage, view)
	writeJSON(w, r, e.logger, http.StatusOK, view)
}

// visibleRequest 读取一次请求，调用方看不到时与「不存在」给出同一个答复。
func (e *endpoints) visibleRequest(
	r *http.Request, id string,
) (pipeline.CapabilityRequest, error) {
	request, err := e.services.Requests.RequestByID(r.Context(), id)
	if err != nil {
		return pipeline.CapabilityRequest{}, err
	}
	if !callerFrom(r.Context()).maySee(request.AgentID) {
		return pipeline.CapabilityRequest{}, notFound("能力请求", id)
	}
	return request, nil
}
