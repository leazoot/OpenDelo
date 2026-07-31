package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * Automation API（REQ-TRUST-005）。
 *
 * 记忆由真实的审批产出：手写一条进库测不出「来源指向产生它的审批」这件事。
 */

// learned 走一次「今后自动允许」，产出一条真实的记忆。
func learned(t *testing.T, all api) httpapi.TrustMemoryView {
	t.Helper()

	item := waiting(t, all)
	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-project", "")
	if response.Code != http.StatusOK {
		t.Fatalf("放行失败：%d %s", response.Code, response.Body.String())
	}

	listed := all.call(t, http.MethodGet, "/v1/trust-memories", "")
	var envelope struct {
		Items []httpapi.TrustMemoryView `json:"items"`
	}
	decodeInto(t, listed, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("库里有 %d 条记忆，期望 1 条", len(envelope.Items))
	}
	return envelope.Items[0]
}

func TestListMemories_ShowsWhereEachOneCameFrom(t *testing.T) {
	// REQ-TRUST-001 AC2/AC3：created_from 指向产生它的审批，
	// Automation 页面据此显示「由你在某次审批中创建」。
	all := newAPI(t)
	memory := learned(t, all)

	if memory.CreatedFrom == "" {
		t.Error("记忆没有来源，页面说不出它是怎么来的")
	}
	if memory.Behavior != string(trust.BehaviorAutoAllow) {
		t.Errorf("行为为 %q，期望 auto_allow", memory.Behavior)
	}
	if memory.RiskCeiling == "high" {
		t.Error("记忆的风险上限是 high —— 那种记忆不该存在")
	}
	if memory.ExpiresAt == "" {
		t.Error("记忆没有到期时刻")
	}
}

func TestListMemories_EmptyLibraryIsAnEmptyListNotAnError(t *testing.T) {
	// Automation 页面在没有记忆时要显示引导性空状态（REQ-TRUST-005 AC3），
	// 因此这里必须是 200 加一个空数组，而不是 404。
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/trust-memories", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}

	var envelope struct {
		Items []httpapi.TrustMemoryView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 0 {
		t.Errorf("空库返回了 %d 条", len(envelope.Items))
	}
	if envelope.Items == nil {
		t.Error("items 是 null 而不是空数组，界面要多写一个判空分支")
	}
}

func TestListMemories_UnknownStatusIsRefused(t *testing.T) {
	all := newAPI(t)

	response := all.call(t, http.MethodGet, "/v1/trust-memories?status=whatever", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestPatchMemory_OnlyTightensNeverWidens(t *testing.T) {
	// REQ-TRUST-002：放宽只能由一次新的审批产生。
	all := newAPI(t)
	memory := learned(t, all)

	tightened := all.call(t, http.MethodPatch, "/v1/trust-memories/"+memory.ID,
		`{"approval_behavior":"always_ask"}`)
	if tightened.Code != http.StatusOK {
		t.Fatalf("收紧失败：%d %s", tightened.Code, tightened.Body.String())
	}
	var view httpapi.TrustMemoryView
	decodeInto(t, tightened, &view)
	if view.Behavior != string(trust.BehaviorAlwaysAsk) {
		t.Errorf("行为为 %q，期望 always_ask", view.Behavior)
	}

	// 反方向不可表达：请求体只接受 always_ask 一个取值。
	widened := all.call(t, http.MethodPatch, "/v1/trust-memories/"+memory.ID,
		`{"approval_behavior":"auto_allow"}`)
	if widened.Code != http.StatusBadRequest {
		t.Fatalf("放宽的状态码为 %d，期望 400", widened.Code)
	}

	still, err := all.backend.Memories.MemoryByID(t.Context(), memory.ID)
	if err != nil {
		t.Fatalf("读取记忆失败：%v", err)
	}
	if still.Behavior != trust.BehaviorAlwaysAsk {
		t.Errorf("库里的行为被改回了 %q", still.Behavior)
	}
}

func TestPatchMemory_TighteningTwiceIsAConflict(t *testing.T) {
	// 已经是「始终询问」的记忆再收紧一次没有意义，且 200 会让调用方
	// 以为自己刚刚改变了什么。
	all := newAPI(t)
	memory := learned(t, all)

	if first := all.call(t, http.MethodPatch, "/v1/trust-memories/"+memory.ID,
		`{"approval_behavior":"always_ask"}`); first.Code != http.StatusOK {
		t.Fatalf("第一次收紧失败：%d", first.Code)
	}

	second := all.call(t, http.MethodPatch, "/v1/trust-memories/"+memory.ID,
		`{"approval_behavior":"always_ask"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409", second.Code)
	}
}

func TestDeleteMemory_RemovesItSoTheNextRequestAsksAgain(t *testing.T) {
	// REQ-TRUST-005 AC1。
	all := newAPI(t)
	memory := learned(t, all)

	response := all.call(t, http.MethodDelete, "/v1/trust-memories/"+memory.ID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("状态码为 %d，期望 204，正文为 %s", response.Code, response.Body.String())
	}

	matched, err := all.backend.Services.Memories.Match(
		t.Context(), memory.AgentID, memory.WorkspaceID, memory.Service, 10)
	if err != nil {
		t.Fatalf("匹配记忆失败：%v", err)
	}
	if len(matched) != 0 {
		t.Errorf("删掉之后还能匹配到 %d 条记忆", len(matched))
	}
}

func TestDeleteMemory_AnUnknownIDIsNotFound(t *testing.T) {
	// 对着一个不存在的 id 返回 204 会让调用方以为自己删掉了什么。
	all := newAPI(t)

	response := all.call(t, http.MethodDelete, "/v1/trust-memories/01J000000000000000NOPE", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
}

func TestPatchMemory_AnUnknownIDIsAConflictNotASilentSuccess(t *testing.T) {
	// 条件更新影响 0 行时不能报成功：调用方会以为自己刚刚收紧了一条记忆。
	all := newAPI(t)

	response := all.call(t, http.MethodPatch, "/v1/trust-memories/01J000000000000000NOPE",
		`{"approval_behavior":"always_ask"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409", response.Code)
	}
}

func TestListMemories_InvalidatedOnesStillCarryTheirReason(t *testing.T) {
	// REQ-TRUST-004 AC2：失效的记忆不消失，页面要说明它为什么失效。
	all := newAPI(t)
	learned(t, all)

	if response := all.call(t, http.MethodDelete,
		"/v1/identities/"+fixtures.DefaultIdentityID, ""); response.Code != http.StatusOK {
		t.Fatalf("断开身份失败：%d", response.Code)
	}

	response := all.call(t, http.MethodGet, "/v1/trust-memories?status=invalidated", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d", response.Code)
	}

	var envelope struct {
		Items []httpapi.TrustMemoryView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 {
		t.Fatalf("失效记忆有 %d 条，期望 1 条", len(envelope.Items))
	}
	if envelope.Items[0].InvalidationReason == "" {
		t.Error("失效记忆没有带上原因，页面只能显示它凭空消失了")
	}
}

func TestDeleteMemory_RecordsTheClearingInTheLedgerBeforeDeleting(t *testing.T) {
	// REQ-UI-007 AC3：清除是破坏性操作，必须留在账本上（用户决定 D-16）。
	all := newAPI(t)
	memory := learned(t, all)

	response := all.call(t, http.MethodDelete, "/v1/trust-memories/"+memory.ID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("状态码为 %d，期望 204，正文为 %s", response.Code, response.Body.String())
	}

	events, err := all.backend.Events.Events(t.Context(), zeroTime, 200)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	cleared := 0
	for _, event := range events {
		if event.Type != audit.EventTrustCleared {
			continue
		}
		cleared++
		// 记的是被清掉的那一条的元数据 —— 删掉之后这些字段就再也读不回来了。
		if event.Service == "" || event.IdentityID == "" {
			t.Errorf("账本里那条记录缺少服务或身份：%+v", event)
		}
	}
	if cleared != 1 {
		t.Fatalf("账本里有 %d 条清除记录，期望 1 条", cleared)
	}
}

func TestDeleteMemory_WhenTheLedgerCannotBeWritten_TheMemoryStays(t *testing.T) {
	// ADR-004：写不进去就不删。一次不在账本上的破坏性操作，
	// 比一次没做成的破坏性操作糟得多。
	all := newAPI(t)
	memory := learned(t, all)
	if err := all.backend.DB.Close(); err != nil {
		t.Fatalf("关闭数据库失败：%v", err)
	}

	response := all.call(t, http.MethodDelete, "/v1/trust-memories/"+memory.ID, "")
	if response.Code == http.StatusNoContent {
		t.Fatal("账本写不进去时仍然删掉了那条记忆")
	}
}
