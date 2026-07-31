package cli_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * connections / leases / audit 的用例。
 *
 * 验收要求「输出与对应 API 结果一致」，所以断言的是**每条记录的每个展示字段
 * 都出现在输出里**，而不是「输出非空」。后者会让一个只打印表头的实现通过。
 */

func limitOf(t *testing.T, seen captured) string {
	t.Helper()

	// ParseQuery 而不是 Parse：后者会把查询串当成路径解析，得到一个空的 Query()。
	parsed, err := url.ParseQuery(seen.rawQuery)
	if err != nil {
		t.Fatalf("解析查询串 %q 失败：%v", seen.rawQuery, err)
	}
	return parsed.Get("limit")
}

func TestConnections_ListsEveryFieldOfEveryIdentity(t *testing.T) {
	var seen captured
	list := gatewayclient.List[httpapi.IdentityView]{Items: []httpapi.IdentityView{
		{
			ID: "identity_1", Service: "github", AccountLabel: "acme-bot",
			Environment: "production", IsDefault: true, Status: "connected",
		},
		{
			ID: "identity_2", Service: "cloudflare", AccountLabel: "acme-dns",
			Environment: "non-production", IsDefault: false, Status: "needs_attention",
		},
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, list, &seen))

	code, stdout, stderr := runCommand(t, dir, "connections")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if seen.path != "/v1/identities" {
		t.Errorf("请求发到了 %s", seen.path)
	}
	for _, identity := range list.Items {
		for _, cell := range []string{
			identity.ID, identity.Service, identity.AccountLabel,
			identity.Environment, identity.Status,
		} {
			if !strings.Contains(stdout, cell) {
				t.Errorf("输出里少了 %q：\n%s", cell, stdout)
			}
		}
	}
}

func TestConnections_Empty_SaysHowToCreateOne(t *testing.T) {
	// 空态给的是下一步，不是「暂无数据」。
	var seen captured
	dir, _ := serveOnLoopback(t,
		fakeGateway(t, http.StatusOK, gatewayclient.List[httpapi.IdentityView]{}, &seen))

	code, stdout, _ := runCommand(t, dir, "connections")

	if code != cli.ExitOK {
		t.Errorf("空列表的退出码为 %d，期望 0 —— 没有身份不是失败", code)
	}
	if !strings.Contains(stdout, "opendelo connect") {
		t.Errorf("空态没有指出下一步：%q", stdout)
	}
}

func TestLeases_ShowsUsageAgainstTheLimit(t *testing.T) {
	// 「还剩几次」是用户决定要不要收回的依据。不限次数与还剩 0 次是两句不同的话。
	var seen captured
	limit := 5
	list := gatewayclient.List[httpapi.LeaseView]{Items: []httpapi.LeaseView{
		{
			ID: "lease_1", Service: "github", Status: "active",
			UsedRequests: 2, RequestLimit: &limit, ExpiresAt: "2026-07-29T10:00:00.000Z",
		},
		{
			ID: "lease_2", Service: "cloudflare", Status: "active",
			UsedRequests: 7, RequestLimit: nil, ExpiresAt: "2026-07-29T11:00:00.000Z",
		},
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, list, &seen))

	code, stdout, stderr := runCommand(t, dir, "leases")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if seen.path != "/v1/leases" {
		t.Errorf("请求发到了 %s", seen.path)
	}
	if !strings.Contains(stdout, "2/5") {
		t.Errorf("有上限的 Lease 没有显示用量：\n%s", stdout)
	}
	if !strings.Contains(stdout, "7/不限") {
		t.Errorf("不限次数的 Lease 显示成了一个数字上限：\n%s", stdout)
	}
	for _, expected := range []string{"lease_1", "lease_2", "2026-07-29T10:00:00.000Z"} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("输出里少了 %q：\n%s", expected, stdout)
		}
	}
}

func TestAudit_ListsEventsAndPassesTheFilterThrough(t *testing.T) {
	var seen captured
	list := gatewayclient.List[httpapi.AuditEventView]{Items: []httpapi.AuditEventView{
		{
			ID: "event_1", OperationID: "01K1OPERATION0000000000001",
			Type: "decision.denied", Service: "github", Operation: "delete_repository",
			Outcome: "blocked", CreatedAt: "2026-07-29T09:00:00.000Z",
		},
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, list, &seen))

	code, stdout, stderr := runCommand(t, dir, "audit", "--service", "github", "--limit", "10")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if !strings.Contains(seen.rawQuery, "service=github") {
		t.Errorf("过滤条件没有随请求发出：%q", seen.rawQuery)
	}
	if limitOf(t, seen) != "10" {
		t.Errorf("limit 为 %q，期望 10", limitOf(t, seen))
	}
	for _, cell := range []string{
		"decision.denied", "github", "delete_repository", "blocked",
		"01K1OPERATION0000000000001", "2026-07-29T09:00:00.000Z",
	} {
		if !strings.Contains(stdout, cell) {
			t.Errorf("输出里少了 %q：\n%s", cell, stdout)
		}
	}
}

func TestAudit_BothFilters_FailsBeforeTouchingTheGateway(t *testing.T) {
	// 两个维度一起过滤会退化成全表扫描。API 侧返回 400，
	// 这里在本地就拦下来，不替用户挑一个。
	var seen captured
	dir, _ := serveOnLoopback(t,
		fakeGateway(t, http.StatusOK, gatewayclient.List[httpapi.AuditEventView]{}, &seen))

	code, _, stderr := runCommand(t, dir, "audit", "--agent", "agent_1", "--service", "github")

	if code == cli.ExitOK {
		t.Fatal("同时给了两个过滤条件却成功了")
	}
	if !strings.Contains(stderr, "二选一") {
		t.Errorf("失败信息没有说明冲突：%s", stderr)
	}
	if seen.method != "" {
		t.Errorf("已经向网关发了 %s %s", seen.method, seen.path)
	}
}

func TestLists_DefaultLimitIsSentExplicitly(t *testing.T) {
	// 不带 limit 的列表查询在十万条账本上会把终端刷爆。
	cases := map[string]string{"connections": "/v1/identities", "leases": "/v1/leases", "audit": "/v1/audit-events"}
	for command, path := range cases {
		t.Run(command, func(t *testing.T) {
			var seen captured
			dir, _ := serveOnLoopback(t,
				fakeGateway(t, http.StatusOK, map[string]any{"items": []any{}}, &seen))

			runCommand(t, dir, command)

			if seen.path != path {
				t.Errorf("请求发到了 %s，期望 %s", seen.path, path)
			}
			if limitOf(t, seen) != "50" {
				t.Errorf("limit 为 %q，期望默认的 50", limitOf(t, seen))
			}
		})
	}
}

func TestLists_GatewayError_IsReportedWithANonZeroExitCode(t *testing.T) {
	failure := map[string]map[string]string{"error": {
		"code": "unauthenticated", "message": "Missing or invalid credentials.",
		"operation_id": "01K1OPERATION0000000000002",
	}}
	for _, command := range []string{"connections", "leases", "audit"} {
		t.Run(command, func(t *testing.T) {
			var seen captured
			dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusUnauthorized, failure, &seen))

			code, _, stderr := runCommand(t, dir, command)

			if code == cli.ExitOK {
				t.Fatal("网关报错却退出码为 0")
			}
			if !strings.Contains(stderr, "01K1OPERATION0000000000002") {
				t.Errorf("错误里没有 operation_id：%s", stderr)
			}
		})
	}
}

func TestLists_OutputCarriesNoSessionToken(t *testing.T) {
	// REQ-CLI-003 AC1。
	var seen captured
	list := gatewayclient.List[httpapi.IdentityView]{Items: []httpapi.IdentityView{
		{
			ID: "identity_1", Service: "github", AccountLabel: sentinel.SentinelToken,
			Environment: "production", Status: "connected",
		},
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, list, &seen))
	token := sessionTokenIn(t, dir)

	_, stdout, stderr := runCommand(t, dir, "connections")

	if strings.Contains(stdout+stderr, token) {
		t.Error("会话令牌出现在了命令输出里")
	}
	if !strings.Contains(stdout, sentinel.SentinelToken) {
		t.Fatalf("网关返回的字段没有出现在输出里，这条用例看不到输出：\n%s", stdout)
	}
}

func TestLists_EmptyFields_AreVisibleAsPlaceholders(t *testing.T) {
	// 空字段留空会让「没有值」与「上一列串位」在终端里看起来一样。
	var seen captured
	list := gatewayclient.List[httpapi.LeaseView]{Items: []httpapi.LeaseView{
		{ID: "lease_1", Service: "", Status: "active", ExpiresAt: "2026-07-29T10:00:00.000Z"},
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, list, &seen))

	_, stdout, _ := runCommand(t, dir, "leases")

	if !strings.Contains(stdout, "—") {
		t.Errorf("空字段没有占位符：\n%s", stdout)
	}
}

func TestLists_Help_GoesToStdoutAndSucceeds(t *testing.T) {
	// REQ-CLI-001 AC1 的退出码语义。
	for _, command := range []string{"connections", "leases", "audit"} {
		t.Run(command, func(t *testing.T) {
			dir := configDirWith(t, 8787)
			code, stdout, _ := runCommand(t, dir, command, "--help")

			if code != cli.ExitOK {
				t.Errorf("--help 的退出码为 %d", code)
			}
			if !strings.Contains(stdout, "limit") {
				t.Errorf("--help 没有列出参数：\n%s", stdout)
			}
		})
	}
}
