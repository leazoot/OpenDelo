package cli

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/scope"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
	"github.com/Runcoor/opendelo/test/fixtures"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Proxy 授权与拦截记账的用例（REQ-PROXY-002、成功标准 S10）。
 *
 * 「没有 Lease 就拒绝」这句话有两种写法：一种真的挨条比过范围，另一种只要
 * 同一个 Agent 对同一个服务有任何一条活跃 Lease 就放行。两者在正常路径上
 * 表现一致，差别只在越界的那一次 —— 因此用例的重心全在越界上。
 */

type proxyHarness struct {
	leases     *proxyLeases
	audits     *proxyAudits
	manager    *lease.Manager
	events     *repo.AuditEvents
	moment     *clock.Fixed
	agentID    string
	identityID string
}

func newProxyHarness(t *testing.T) proxyHarness {
	t.Helper()

	// 走公共的链条夹具：Lease 的外键要求 agents 与 identities 都已存在，而这两张表
	// 各自还有自己的前置行。自己再造一份等于让本文件对「一条 Lease 签得出来需要
	// 什么」另有一套看法。
	database := fixtures.SeededRequestChain(t)
	moment := clock.NewFixed(time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC))
	ids := ulid.New(moment)

	manager, err := lease.NewManager(lease.Options{
		Leases: repo.NewLeases(database), Clock: moment, IDs: ids,
	})
	if err != nil {
		t.Fatalf("构造 Lease Manager 失败：%v", err)
	}
	events := repo.NewAuditEvents(database)
	recorder, err := audit.NewRecorder(events, moment, ids)
	if err != nil {
		t.Fatalf("构造账本失败：%v", err)
	}

	return proxyHarness{
		leases: &proxyLeases{leases: manager}, audits: &proxyAudits{recorder: recorder},
		manager: manager, events: events, moment: moment,
		agentID: fixtures.DefaultAgentID, identityID: fixtures.DefaultIdentityID,
	}
}

// 工作区取夹具里的那一个：账本的 workspace_id 是外键，凭空写一个进去写不进去。
const testWorkspaceID = fixtures.DefaultWorkspaceID

// issueLease 签一条罩住某个操作与资源的 Lease。
func issueLease(t *testing.T, h proxyHarness, operation string, resource map[string]string) lease.Lease {
	t.Helper()

	granted := scope.Scope{
		AgentID: h.agentID, WorkspaceID: testWorkspaceID, Service: "github",
		IdentityID: h.identityID, Account: "runcoor",
		Resource: resource, ResourceKey: intent.ResourceKeyOf(resource),
		Operation: operation,
		NotBefore: h.moment.Now(), ExpiresAt: h.moment.Now().Add(15 * time.Minute),
		RequestLimit: 5, Environment: matcher.EnvironmentNonProduction,
		RiskCeiling: "low",
	}
	issued, err := h.manager.Issue(t.Context(), lease.IssueRequest{Granted: granted})
	if err != nil {
		t.Fatalf("签发 Lease 失败：%v", err)
	}
	return issued
}

func (h proxyHarness) caller() proxy.Caller {
	return proxy.Caller{AgentID: h.agentID, WorkspaceID: testWorkspaceID}
}

func TestProxyAuthorize_RequestInsideTheLease_IsGrantedAndCounted(t *testing.T) {
	h := newProxyHarness(t)
	resource := map[string]string{"owner": "runcoor", "repo": "opendelo"}
	issued := issueLease(t, h, "read_repository", resource)

	grant, err := h.leases.Authorize(t.Context(), h.caller(), proxy.Route{
		Service: "github", Operation: "read_repository", Resource: resource,
	})
	if err != nil {
		t.Fatalf("范围内的请求被拒：%v", err)
	}
	if grant.LeaseID != issued.ID {
		t.Errorf("授权用的是 Lease %q，期望 %q", grant.LeaseID, issued.ID)
	}
	if grant.IdentityID != h.identityID {
		t.Errorf("授权带出的身份为 %q，期望 %q", grant.IdentityID, h.identityID)
	}

	// 计量必须真的发生：不计数的话次数上限形同虚设。
	after, err := h.manager.ByID(t.Context(), issued.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if after.UsedRequests != 1 {
		t.Errorf("使用次数为 %d，期望 1", after.UsedRequests)
	}
}

func TestProxyAuthorize_OutsideTheLease_IsRefused(t *testing.T) {
	resource := map[string]string{"owner": "runcoor", "repo": "opendelo"}

	cases := []struct {
		name  string
		route proxy.Route
		agent string
	}{
		{"另一个操作", proxy.Route{Service: "github", Operation: "delete_repository", Resource: resource}, ""},
		{
			"另一个资源",
			proxy.Route{Service: "github", Operation: "read_repository", Resource: map[string]string{"owner": "runcoor", "repo": "other"}},
			"",
		},
		{"另一个服务", proxy.Route{Service: "cloudflare", Operation: "read_repository", Resource: resource}, ""},
		{"另一个 Agent", proxy.Route{Service: "github", Operation: "read_repository", Resource: resource}, "agent_someone_else"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newProxyHarness(t)
			issued := issueLease(t, h, "read_repository", resource)

			caller := h.caller()
			// 留空表示沿用本次夹具的 Agent；只有「另一个 Agent」那一条要换掉它。
			if testCase.agent != "" {
				caller.AgentID = testCase.agent
			}
			grant, err := h.leases.Authorize(t.Context(), caller, testCase.route)

			if !apperr.Is(err, apperr.CodeCredentialNotAuthorized) {
				t.Fatalf("错误码为 %s，期望 credential_not_authorized（%v）", apperr.CodeOf(err), err)
			}
			if grant.LeaseID != "" || grant.IdentityID != "" {
				t.Errorf("拒绝时仍然给出了授权 %+v", grant)
			}

			// 越界的尝试不得消耗别人的次数。
			after, byErr := h.manager.ByID(t.Context(), issued.ID)
			if byErr != nil {
				t.Fatalf("读取 Lease 失败：%v", byErr)
			}
			if after.UsedRequests != 0 {
				t.Errorf("越界的请求消耗了 %d 次配额", after.UsedRequests)
			}
		})
	}
}

func TestProxyAuthorize_ExpiredLease_IsRefused(t *testing.T) {
	h := newProxyHarness(t)
	resource := map[string]string{"owner": "runcoor", "repo": "opendelo"}
	issueLease(t, h, "read_repository", resource)

	h.moment.Advance(30 * time.Minute)

	_, err := h.leases.Authorize(t.Context(), h.caller(), proxy.Route{
		Service: "github", Operation: "read_repository", Resource: resource,
	})
	if !apperr.Is(err, apperr.CodeCredentialNotAuthorized) {
		t.Errorf("过期的 Lease 仍然授权了：%v", err)
	}
}

func TestProxyRecordBlocked_WritesMetadataWithoutTheQueryString(t *testing.T) {
	// 记录 URL 时只保留 path。
	// 查询串里可能带凭据，而这条记录是给人看的。
	h := newProxyHarness(t)

	ctx := logging.WithOperationID(t.Context(), "operation_blocked_test")
	err := h.audits.RecordBlocked(ctx, proxy.Blocked{
		Caller: h.caller(),
		Target: proxy.Target{
			Host: "api.github.com", Method: "GET", Path: "/repos/runcoor/opendelo",
		},
		Route:  proxy.Route{Service: "github", Operation: "read_repository"},
		Reason: "credential_not_authorized",
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}

	written, err := h.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	if len(written) != 1 {
		t.Fatalf("账本里有 %d 条记录，期望 1", len(written))
	}

	event := written[0]
	if event.Outcome != audit.OutcomeBlocked {
		t.Errorf("Outcome 为 %q，期望 blocked", event.Outcome)
	}
	// 这一条是账本能证明「拦截发生在出站之前」的唯一依据。
	if event.ResponseStatus != 0 {
		t.Errorf("ResponseStatus 为 %d，期望 0（没有发出过外部请求）", event.ResponseStatus)
	}
	if event.AgentID != h.agentID || event.Service != "github" {
		t.Errorf("记录没有指出是谁去了哪里：%+v", event)
	}
	if !strings.Contains(event.Metadata, "/repos/runcoor/opendelo") {
		t.Errorf("元数据里没有路径：%q", event.Metadata)
	}
}

func TestProxyRecordExecuted_LandsInTheLedgerAsAnExecution(t *testing.T) {
	// R-47。8789（MCP）那条路一直在写 adapter.executed，8788 之前只有访问日志，
	// 于是经代理的每一次成功转发在账本上都是无痕的。
	h := newProxyHarness(t)
	// 账本的 lease_id 是外键：凭空写一个字符串进去这条记录根本落不了地。
	issued := issueLease(t, h, "read_repository", map[string]string{"owner": "runcoor", "repo": "opendelo"})

	ctx := logging.WithOperationID(t.Context(), "operation_executed_test")
	err := h.audits.RecordExecuted(ctx, proxy.Executed{
		Caller: h.caller(),
		Target: proxy.Target{
			Host: "api.github.com", Method: "GET",
			Path: "/repos/runcoor/opendelo",
			// 有些服务把令牌放在查询串里 —— 它一路带到这里，但不该进账本。
			Query: url.Values{"access_token": []string{sentinel.SentinelToken}},
		},
		Route: proxy.Route{
			Service: "github", Operation: "read_repository",
			Resource: map[string]string{"owner": "runcoor", "repo": "opendelo"},
		},
		Grant:          proxy.Grant{LeaseID: issued.ID, IdentityID: h.identityID},
		UpstreamStatus: 201,
		Succeeded:      true,
		Duration:       250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}

	written, err := h.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	if len(written) != 1 {
		t.Fatalf("账本里有 %d 条记录，期望 1", len(written))
	}

	event := written[0]
	switch {
	case event.Type != audit.EventAdapterExecuted:
		t.Errorf("事件类型为 %q，期望 adapter.executed", event.Type)
	case event.Outcome != audit.OutcomeSucceeded:
		t.Errorf("Outcome 为 %q，期望 succeeded", event.Outcome)
	case event.LeaseID != issued.ID:
		// 「凭什么发出去的」是事后唯一要问的问题。
		t.Errorf("LeaseID 为 %q", event.LeaseID)
	case event.ResponseStatus != 201:
		// 记的是上游真答的那个数字，不是给 Agent 的 200/502 映射（R-44）。
		t.Errorf("ResponseStatus 为 %d，期望 201", event.ResponseStatus)
	case !strings.Contains(event.Metadata, `"face":"proxy"`):
		// 与 8789 那条路成对：账本上两个面除此之外看不出区别。
		t.Errorf("元数据里没有标出是哪个面：%q", event.Metadata)
	case event.Duration != 250*time.Millisecond:
		// 账本上「耗时」那一格是给人看的：0 ms 读起来是「这次没花时间」，
		// 而不是「没测」（R-48）。
		t.Errorf("耗时为 %v，期望 250ms", event.Duration)
	}
	// 查询串不进账本 —— 有些服务把令牌放在那里。
	if strings.Contains(event.Metadata, sentinel.SentinelToken) {
		t.Errorf("查询串进了账本：%q", event.Metadata)
	}
}

func TestProxyRecordExecuted_UpstreamRefusal_IsRecordedAsFailed(t *testing.T) {
	// 上游答了但没答成也是一次执行。一律记成成功会让账本看不出这次调用其实没成，
	// 而账本是唯一的事后依据。
	h := newProxyHarness(t)
	issued := issueLease(t, h, "create_repository", map[string]string{"owner": "runcoor"})

	ctx := logging.WithOperationID(t.Context(), "operation_executed_failed")
	err := h.audits.RecordExecuted(ctx, proxy.Executed{
		Caller:         h.caller(),
		Target:         proxy.Target{Host: "api.github.com", Method: "POST", Path: "/repos"},
		Route:          proxy.Route{Service: "github", Operation: "create_repository"},
		Grant:          proxy.Grant{LeaseID: issued.ID, IdentityID: h.identityID},
		UpstreamStatus: 422,
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}

	written, err := h.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}
	if len(written) != 1 {
		t.Fatalf("账本里有 %d 条记录，期望 1", len(written))
	}
	if written[0].Outcome != audit.OutcomeFailed {
		t.Errorf("Outcome 为 %q，期望 failed", written[0].Outcome)
	}
	if written[0].ResponseStatus != 422 {
		t.Errorf("ResponseStatus 为 %d，期望 422", written[0].ResponseStatus)
	}
}

func TestProxyRecordBlocked_NeverWritesACredential(t *testing.T) {
	// 八个面之一：审计（REQ-NFR-002 AC1）。哨兵出现在最容易被顺手带上的位置 ——
	// 查询串与拒绝理由。
	h := newProxyHarness(t)

	ctx := logging.WithOperationID(t.Context(), "operation_blocked_test")
	err := h.audits.RecordBlocked(ctx, proxy.Blocked{
		Caller: h.caller(),
		Target: proxy.Target{
			Host: "api.github.com", Method: "GET",
			Path: "/repos/runcoor/opendelo",
		},
		Route:  proxy.Route{Service: "github", Operation: "read_repository"},
		Reason: "credential_not_authorized",
	})
	if err != nil {
		t.Fatalf("记账失败：%v", err)
	}

	written, err := h.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("读取账本失败：%v", err)
	}

	encoded, err := json.Marshal(written)
	if err != nil {
		t.Fatalf("序列化账本失败：%v", err)
	}
	for _, value := range []string{sentinel.SentinelToken, sentinel.SentinelAPIKey} {
		if strings.Contains(string(encoded), value) {
			t.Errorf("账本里出现了凭据哨兵 %q", value)
		}
	}
}
