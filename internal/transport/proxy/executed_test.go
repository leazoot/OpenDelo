package proxy

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

/*
 * 经代理的成功转发要入账（R-47，不可协商约束第 6 条「无未审计路径」）。
 *
 * 在这之前，8788 的成功转发在账本上是无痕的：决策事件说的是「准许了什么」，
 * Lease 事件说的是「签发与计次」，两者都发生在请求发出**之前**；发出去了没有、
 * 外部服务答了什么，只有一条访问日志知道，而日志按 `.claude/rules/backend.md`
 * §8.6 不能替代审计。8789（MCP）那条路一直在记，两个面因此对不上账。
 */

const sentinelBody = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"

func TestServeHTTP_SuccessfulForward_IsRecordedInTheLedger(t *testing.T) {
	h := newHarness(t)
	h.exchange.reply = Reply{
		StatusCode: http.StatusOK, ContentType: "application/json",
		Body: []byte(`{"name":"console"}`), UpstreamStatus: http.StatusCreated,
	}

	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.executed) != 1 {
		t.Fatalf("账本上记了 %d 条执行，期望一条", len(h.audits.executed))
	}
	got := h.audits.executed[0]
	switch {
	case got.Target.Host != "api.github.com":
		t.Errorf("host 为 %q", got.Target.Host)
	case got.Target.Path != "/repos/acme/console":
		t.Errorf("path 为 %q", got.Target.Path)
	case got.Grant.LeaseID != "lease_1":
		// 少了它，账本上这条执行追不回到那份授权 —— 而「凭什么发出去的」
		// 正是事后唯一要问的问题。
		t.Errorf("lease_id 为 %q", got.Grant.LeaseID)
	case got.UpstreamStatus != http.StatusCreated:
		// 记的是外部服务真答的那个数字，不是给 Agent 的 200/502 映射（R-44）。
		t.Errorf("上游状态码为 %d，期望 201", got.UpstreamStatus)
	case !got.Succeeded:
		t.Error("这一次是成功的，却记成了失败")
	}
}

func TestServeHTTP_UpstreamRefused_IsStillRecordedAsAnExecution(t *testing.T) {
	// 上游答了但没答成也是一次执行：它发生过，可能已经改变了外部状态。
	// 一律记成成功会让账本看不出这次调用其实没成。
	h := newHarness(t)
	h.exchange.reply = Reply{
		StatusCode: http.StatusBadGateway, ContentType: "application/json",
		Body: []byte(`{"error":"upstream"}`), UpstreamStatus: http.StatusUnprocessableEntity,
	}

	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.executed) != 1 {
		t.Fatalf("账本上记了 %d 条执行，期望一条", len(h.audits.executed))
	}
	if h.audits.executed[0].Succeeded {
		t.Error("上游答了 422，账本却记成成功")
	}
	if h.audits.executed[0].UpstreamStatus != http.StatusUnprocessableEntity {
		t.Errorf("上游状态码为 %d，期望 422", h.audits.executed[0].UpstreamStatus)
	}
}

func TestServeHTTP_LedgerWriteFails_TheReplyIsNotHandedToTheAgent(t *testing.T) {
	// 出站已经发生，收不回来了。但一次外部服务的答复换来账本上的一个空洞，
	// 这笔交换不该由网关替用户做主（ADR-004 的同一条理由）。
	h := newHarness(t)
	h.audits.executedErr = errors.New("账本写不进去")
	h.exchange.reply = Reply{
		StatusCode: http.StatusOK, ContentType: "application/json",
		Body: []byte(`{"name":"console"}`),
	}

	recorder := h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if recorder.Code == http.StatusOK {
		t.Error("账本写不进去，响应仍然交给了 Agent")
	}
	if strings.Contains(recorder.Body.String(), "console") {
		t.Errorf("外部服务的答复漏了出去：%s", recorder.Body.String())
	}
	if !strings.Contains(h.logs.String(), "forwarded_unaudited") {
		// 这条日志是运维唯一的信号：出站发生过，账本上却没有。
		t.Errorf("访问日志里没有标出这次转发未入账：\n%s", h.logs.String())
	}
}

func TestServeHTTP_ExecutionEvent_CarriesNoRequestOrResponseBody(t *testing.T) {
	// 账本记的是元数据（PRD §22.1）。正文可能带凭据 —— 请求体是 Agent 送来的，
	// 响应体是外部服务答的，两者都不该进账本。
	h := newHarness(t)
	h.exchange.reply = Reply{
		StatusCode: http.StatusOK, ContentType: "application/json",
		Body: []byte(`{"token":"` + sentinelBody + `"}`),
	}

	h.do(t, proxyRequest(t, http.MethodPost,
		"http://api.github.com/repos/acme/console",
		strings.NewReader(`{"secret":"`+sentinelBody+`"}`)))

	if len(h.audits.executed) != 1 {
		t.Fatalf("账本上记了 %d 条执行，期望一条", len(h.audits.executed))
	}
	// Executed 的每一个字段逐个看：结构体上根本没有正文字段是这条断言想要的
	// 结果，但字段是会被加上去的，用例守的是那一天。
	got := h.audits.executed[0]
	for _, text := range []string{
		got.Target.Host, got.Target.Path, got.Target.Method,
		got.Route.Service, got.Route.Operation,
		got.Grant.LeaseID, got.Grant.IdentityID,
		got.Caller.AgentID, got.Caller.WorkspaceID,
	} {
		if strings.Contains(text, sentinelBody) {
			t.Errorf("执行事件里出现了哨兵：%q", text)
		}
	}
	for key, value := range got.Route.Resource {
		if strings.Contains(value, sentinelBody) {
			t.Errorf("资源维度 %s 里出现了哨兵：%q", key, value)
		}
	}
}

func TestServeHTTP_ExecutionEvent_CarriesHowLongTheOutboundTook(t *testing.T) {
	// 账本上「耗时」那一格是给人看的。经代理的执行恒为 0 ms、旁边经 MCP 的
	// 是真实数字时，读起来是「这次没花时间」而不是「没测」（R-48）。
	h := newHarness(t)
	h.exchange.elapsed = 250 * time.Millisecond

	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.executed) != 1 {
		t.Fatalf("账本上记了 %d 条执行，期望一条", len(h.audits.executed))
	}
	if got := h.audits.executed[0].Duration; got != 250*time.Millisecond {
		t.Errorf("耗时记成 %v，期望 250ms —— 出站前后各取一次时间才量得到", got)
	}
}

func TestServeHTTP_ExecutionEvent_TimesOnlyTheOutbound(t *testing.T) {
	// 计时的起点在 exchange.Send 之前，不在整个处理链之前：认证、认服务、
	// 匹配 Lease 都发生在出站之前，把它们算进「这次出站花了多久」是记错了。
	h := newHarness(t)
	h.leases.elapsed = 3 * time.Second // 匹配 Lease 花掉的时间，发生在出站之前
	h.exchange.elapsed = 40 * time.Millisecond

	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	if len(h.audits.executed) != 1 {
		t.Fatalf("账本上记了 %d 条执行，期望一条", len(h.audits.executed))
	}
	if got := h.audits.executed[0].Duration; got != 40*time.Millisecond {
		t.Errorf("耗时记成 %v，期望 40ms —— 出站之前花掉的时间被算进去了", got)
	}
}
