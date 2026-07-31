package httpapi

import (
	"context"
	"net/http"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Approval API：待审批列表与五个决策端点（PRD §27、REQ-APPROVAL-002、REQ-API-004）。
 *
 * 五个决策端点各自对应一个固定的操作，路径里带着它 —— 而不是一个通用端点
 * 加一个 action 字段。这样「哪些操作存在」在路由表上就能数清楚，
 * 也不存在往 action 里塞第六个取值的路子。
 *
 * 第五个是「始终要求确认」（用户决定 D-15 ③）。它在 PRD §13.2 的五种操作里，
 * 但 §27 的端点清单漏了它，于是 ActionAlwaysAsk 一直无路可走。**这个端点不自己
 * 判断风险等级**：哪些操作可用只有 core/approval.Actions 一个答案，接入面再判一次
 * 就是第二个答案。
 */

// settleAction 把五个端点各自绑定到一个操作。
type settleAction struct {
	path   string
	action approval.Action
}

var settleActions = []settleAction{
	{path: "allow-once", action: approval.ActionAllowOnce},
	{path: "allow-task", action: approval.ActionAllowUntilTaskEnd},
	{path: "allow-project", action: approval.ActionAutoAllowInProject},
	{path: "always-ask", action: approval.ActionAlwaysAsk},
	{path: "deny", action: approval.ActionDeny},
}

// settleBody 是四个决策端点共同的请求体。
//
// 没有 action 字段：操作由路径决定，请求体改不了它。也没有
// strong_auth_completed —— 那一位**不可提交**，带上它 DisallowUnknownFields
// 会让请求直接 400，调用方因此不会以为自己声明的认证生效了（见 strongAuthCompleted）。
type settleBody struct {
	// Confirmations 是这次提交已经完成的确认次数，谨慎模式的高风险要 2 次。
	Confirmations int `json:"confirmations"`
}

// strongAuthCompleted 报告这台 Gateway 上现在有没有人刚刚当面解过锁。
//
// **强认证是服务端的事实，不是请求体里的一个声明**（REQ-APPROVAL-005 AC1）：
// 调用方自称通过了认证，与没有认证是同一回事 —— 会话令牌已经在手的进程
// 照样能把那一位置真。这里问的是保险库，主密码在 `/v1/vault/unlock` 上
// 由 Gateway 校验过（用户决定 D-14 方案 C）。
//
// 没有装配保险库时返回 false：认不出「有没有人在场」就当成不在场（Fail Closed）。
func (e *endpoints) strongAuthCompleted() bool {
	return e.services.Vault != nil && e.services.Vault.IsUnlocked()
}

// listApprovals 返回等待处理的审批项（Gate 页面的数据来源）。
//
// **Agent 看不到这个列表**：审批是人的事，把「谁在等你点头」告诉发起方
// 等于给它一份可以逐条催的清单（REQ-DECIDE-004 的边界）。
func (e *endpoints) listApprovals(w http.ResponseWriter, r *http.Request) {
	if callerFrom(r.Context()).IsAgent() {
		e.fail(w, r, apperr.New(apperr.CodeForbidden).
			WithDetail("Agent 不能访问审批端点"))
		return
	}

	limit, err := limitFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	pending, err := e.services.Approvals.Pending(r.Context(), limit)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	items := make([]ApprovalView, 0, len(pending))
	for _, item := range pending {
		view, buildErr := e.expand(r.Context(), item)
		if buildErr != nil {
			e.fail(w, r, buildErr)
			return
		}
		items = append(items, view)
	}
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[ApprovalView]{Items: items})
}

// settle 是四个决策端点的处理器，操作由路由固定传入。
func (e *endpoints) settle(action approval.Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if callerFrom(r.Context()).IsAgent() {
			e.fail(w, r, apperr.New(apperr.CodeForbidden).
				WithDetail("Agent 不能决定自己的请求"))
			return
		}

		id, err := pathID(r)
		if err != nil {
			e.fail(w, r, err)
			return
		}

		body := settleBody{Confirmations: 1}
		if r.ContentLength > 0 {
			if decodeErr := decodeBody(r, &body); decodeErr != nil {
				e.fail(w, r, decodeErr)
				return
			}
		}

		result, err := e.services.Pipeline.SettleApproval(r.Context(), pipeline.SettleInput{
			ApprovalID:          id,
			Action:              action,
			StrongAuthCompleted: e.strongAuthCompleted(),
			Confirmations:       body.Confirmations,
		})
		if err != nil {
			e.fail(w, r, err)
			return
		}

		envelope := e.settlementView(result)
		// 重放不广播：那一次的事件在首次提交时已经播过了，再播一次会让
		// Gate 页面看起来发生了两次决定。
		if !result.Replayed {
			e.publish(r, EventPassage, envelope.Approval)
			if envelope.Lease != nil {
				e.publish(r, EventLease, *envelope.Lease)
			}
		}
		writeJSON(w, r, e.logger, http.StatusOK, envelope)
	}
}

func (e *endpoints) settlementView(result pipeline.SettleResult) settlementEnvelope {
	record := decisionView(result.Decision)
	request := requestView(result.Request, &record,
		e.withheldOperations(result.Request.Service, result.Request.Operation))

	envelope := settlementEnvelope{
		Approval: approvalView(result.Approval,
			approval.Actions(result.Decision.RiskLevel), nil, &record),
		Request:  &request,
		Replayed: result.Replayed,
	}
	if result.Lease != nil {
		view := leaseView(*result.Lease)
		envelope.Lease = &view
	}
	if result.Memory != nil {
		envelope.TrustMemoryID = result.Memory.ID
	}
	return envelope
}

// expand 把一个审批项补齐成 Gate 页面要渲染的内容。
//
// 决策读不出来时返回错误而不是给一个只有 id 的空壳：审批界面必须说清
// 「哪个 Agent、什么操作、什么风险」（REQ-APPROVAL-001），
// 缺了这些的卡片会让人在不知道自己在批什么的情况下点头。
func (e *endpoints) expand(
	ctx context.Context, item approval.Approval,
) (ApprovalView, error) {
	record, err := e.services.Decisions.DecisionByID(ctx, item.DecisionID)
	if err != nil {
		return ApprovalView{}, err
	}
	request, err := e.services.Requests.RequestByID(ctx, record.CapabilityRequestID)
	if err != nil {
		return ApprovalView{}, err
	}

	view := decisionView(record)
	requested := requestView(request, &view,
		e.withheldOperations(request.Service, request.Operation))
	return approvalView(item, availableActions(record), &requested, &view), nil
}

// availableActions 是这个决策下界面可以提供的操作。
//
// 风险等级取自决策记录 —— 让界面自己传一个等级过来，等于把
// 「高风险不提供学习类操作」这条规则交给了调用方（REQ-APPROVAL-002）。
func availableActions(record decision.Decision) []approval.Action {
	return approval.Actions(risk.Level(record.RiskLevel))
}
