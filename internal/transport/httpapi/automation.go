package httpapi

import (
	"net/http"

	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * Automation API：已学习授权的查看、收紧与删除（PRD §27、REQ-TRUST-005）。
 *
 * 这里只有三个动作，且**没有一个能让权限变多**：看、改成「始终询问」、删掉。
 * 想让一条记忆覆盖更多，只能重新走一次审批 —— `core/trust` 连修改范围的方法
 * 都不提供（REQ-TRUST-002）。
 */

// patchMemoryBody 是 PATCH /v1/trust-memories/:id 的请求体。
//
// 只有一个字段，且只接受一个取值。放宽的方向在这里连表达都表达不出来。
type patchMemoryBody struct {
	ApprovalBehavior string `json:"approval_behavior"`
}

// listMemories 列出授权记忆（Automation 页面）。
//
// 默认只看生效中的；`status=invalidated` 用来看已失效的那些 ——
// 失效的记忆不消失，页面要显示它为什么失效（REQ-TRUST-004 AC2）。
func (e *endpoints) listMemories(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "自动化"); err != nil {
		return
	}

	limit, err := limitFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	status := trust.StatusActive
	if raw := r.URL.Query().Get("status"); raw != "" {
		status = trust.Status(raw)
	}

	memories, err := e.services.Memories.ByStatus(r.Context(), status, limit)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	items := make([]TrustMemoryView, 0, len(memories))
	for _, memory := range memories {
		items = append(items, memoryView(memory))
	}
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[TrustMemoryView]{Items: items})
}

// memoryByID 把 PATCH 与 DELETE 分派到各自的处理器。
//
// 两个方法共用一个路径模式，而 allowMethods 只管放不放行、不管分派。
func (e *endpoints) memoryByID(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		e.deleteMemory(w, r)
		return
	}
	e.tightenMemory(w, r)
}

// tightenMemory 把一条记忆改成「始终询问」（REQ-TRUST-005）。
//
// 只接受这一个取值：改回「自动允许」是放宽，而放宽只能由一次新的审批产生。
func (e *endpoints) tightenMemory(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "自动化"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	var body patchMemoryBody
	if err = decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	if trust.Behavior(body.ApprovalBehavior) != trust.BehaviorAlwaysAsk {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("approval_behavior 只能改成 always_ask；放宽要重新走一次审批"),
			"approval_behavior")
		return
	}

	tightened, err := e.services.Memories.TightenBehavior(r.Context(), id)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK, memoryView(tightened))
}

// deleteMemory 删除一条记忆（REQ-TRUST-005 AC1）。删掉之后对应请求下次进入审批。
func (e *endpoints) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "自动化"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	// 清除是破坏性操作，先记账本再删行（REQ-UI-007 AC3、ADR-004）：
	// 删掉之后这条记忆的服务、身份与项目就再也读不回来了。顺序与整条判断
	// 都在 core/pipeline 里，这一层只做协议转换。
	if err = e.services.Pipeline.ClearTrustMemory(
		r.Context(), id, logging.OperationIDFrom(r.Context())); err != nil {
		e.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
