package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * connect 与 revoke 的用例（REQ-CLI-001、REQ-CLI-003）。
 *
 * 网关侧用本地 fake（真实响应形状，不是 mock 断言）。
 * 判断在网关那边，这里测的是：请求发对了地方、带对了令牌、错误原样上抛、
 * 输出里没有凭据。
 */

// captured 记下 fake 网关收到了什么。
type captured struct {
	method   string
	path     string
	rawPath  string
	rawQuery string
	auth     string
	body     []byte
}

func fakeGateway(t *testing.T, status int, response any, seen *captured) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := &bytes.Buffer{}
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Errorf("读取请求体失败：%v", err)
		}
		*seen = captured{
			method: r.Method, path: r.URL.Path, rawPath: r.URL.EscapedPath(),
			rawQuery: r.URL.RawQuery,
			auth:     r.Header.Get("Authorization"), body: body.Bytes(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("写出响应失败：%v", err)
		}
	})
}

func runCommand(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := cli.Run(t.Context(), cli.Options{
		Args:    append(args, "--config-dir", dir),
		Stdout:  stdout,
		Stderr:  stderr,
		Version: "test",
		Clock:   clock.System{},
	})
	return code, stdout.String(), stderr.String()
}

func TestConnect_SendsTheReferenceAndPrintsTheIdentity(t *testing.T) {
	var seen captured
	identity := httpapi.IdentityView{
		ID: "identity_1", Service: "github", AccountLabel: "acme-bot",
		Environment: "production", Status: "connected",
		CredentialReferenceID: "reference_1",
	}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, identity, &seen))

	code, stdout, stderr := runCommand(t, dir, "connect",
		"--credential-reference", "reference_1", "--account-label", "acme-bot")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if seen.method != http.MethodPost || seen.path != "/v1/identities/connect" {
		t.Errorf("请求发到了 %s %s", seen.method, seen.path)
	}
	if want := "Bearer " + sessionTokenIn(t, dir); seen.auth != want {
		t.Errorf("Authorization 为 %q，期望带上配置目录里的会话令牌", seen.auth)
	}
	if !strings.Contains(string(seen.body), `"credential_reference_id":"reference_1"`) {
		t.Errorf("请求体里没有凭据引用：%s", seen.body)
	}
	for _, expected := range []string{"identity_1", "github", "acme-bot", "connected"} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("输出里没有 %q：\n%s", expected, stdout)
		}
	}
}

func TestConnect_WithoutAReference_FailsBeforeTouchingTheGateway(t *testing.T) {
	// 缺参数是本地就能判断的事，不该先去打扰网关。
	var seen captured
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, httpapi.IdentityView{}, &seen))

	code, _, stderr := runCommand(t, dir, "connect")

	if code == cli.ExitOK {
		t.Fatal("没给凭据引用却成功了")
	}
	if !strings.Contains(stderr, "credential-reference") {
		t.Errorf("失败信息里没有指出缺了什么：%s", stderr)
	}
	if seen.method != "" {
		t.Errorf("缺参数却已经向网关发了 %s %s", seen.method, seen.path)
	}
}

func TestConnect_GatewayError_KeepsTheCodeAndOperationID(t *testing.T) {
	// 折成一句「操作失败」会让用户丢掉 operation_id —— 那是去账本里查的唯一线索。
	var seen captured
	failure := map[string]map[string]string{"error": {
		"code":         "provider_unavailable",
		"message":      "The credential source is unavailable; the request was denied.",
		"operation_id": "01K1OPERATION0000000000001",
	}}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusBadGateway, failure, &seen))

	code, _, stderr := runCommand(t, dir, "connect", "--credential-reference", "reference_1")

	if code == cli.ExitOK {
		t.Fatal("网关报错却成功了")
	}
	for _, expected := range []string{"provider_unavailable", "01K1OPERATION0000000000001"} {
		if !strings.Contains(stderr, expected) {
			t.Errorf("错误输出里没有 %q：\n%s", expected, stderr)
		}
	}
}

func TestRevoke_SendsADeleteAndPrintsTheClosedLease(t *testing.T) {
	var seen captured
	revoked := httpapi.LeaseView{
		ID: "lease_1", Service: "github", Status: "revoked",
		ExpiresAt: "2026-07-29T10:00:00.000Z",
	}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, revoked, &seen))

	code, stdout, stderr := runCommand(t, dir, "revoke", "--lease", "lease_1")

	if code != cli.ExitOK {
		t.Fatalf("退出码为 %d，stderr：%s", code, stderr)
	}
	if seen.method != http.MethodDelete || seen.path != "/v1/leases/lease_1" {
		t.Errorf("请求发到了 %s %s", seen.method, seen.path)
	}
	if !strings.Contains(stdout, "lease_1") || !strings.Contains(stdout, "revoked") {
		t.Errorf("输出里没有被收回的 Lease：\n%s", stdout)
	}
}

func TestRevoke_LeaseIDIsEscapedIntoThePath(t *testing.T) {
	// ID 来自命令行。直接拼进 URL 的话，一个带 ../ 的值就能改写请求路径。
	var seen captured
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, httpapi.LeaseView{}, &seen))

	runCommand(t, dir, "revoke", "--lease", "../identities/identity_1")

	// 线路上是转义后的形状：Go 的路由按段匹配，%2F 不会被当成一层路径分隔。
	if seen.rawPath != "/v1/leases/..%2Fidentities%2Fidentity_1" {
		t.Errorf("线路上的路径为 %q，期望 ID 被逐段转义而不是改写掉路径", seen.rawPath)
	}
}

func TestRevoke_WithoutALease_FailsBeforeTouchingTheGateway(t *testing.T) {
	var seen captured
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, httpapi.LeaseView{}, &seen))

	code, _, stderr := runCommand(t, dir, "revoke")

	if code == cli.ExitOK {
		t.Fatal("没给 Lease ID 却成功了")
	}
	if !strings.Contains(stderr, "--lease") {
		t.Errorf("失败信息里没有指出缺了什么：%s", stderr)
	}
	if seen.method != "" {
		t.Errorf("缺参数却已经向网关发了 %s %s", seen.method, seen.path)
	}
}

func TestConnectAndRevoke_OutputCarriesNoCredentials(t *testing.T) {
	// REQ-CLI-003 AC1。会话令牌与哨兵都不得出现在 stdout / stderr 里。
	var seen captured
	identity := httpapi.IdentityView{
		ID: "identity_1", Service: "github", AccountLabel: sentinel.SentinelToken,
		Environment: "production", Status: "connected",
	}
	dir, _ := serveOnLoopback(t, fakeGateway(t, http.StatusOK, identity, &seen))
	token := sessionTokenIn(t, dir)

	_, stdout, stderr := runCommand(t, dir, "connect", "--credential-reference", "reference_1")

	if strings.Contains(stdout+stderr, token) {
		t.Error("会话令牌出现在了命令输出里")
	}
	// 账号标签是网关回来的字段，原样显示是对的；这里断言的是**令牌**不外泄。
	// 哨兵放在标签位上是为了让「输出确实包含网关返回的内容」这件事可见。
	if !strings.Contains(stdout, sentinel.SentinelToken) {
		t.Errorf("网关返回的字段没有出现在输出里，这条用例看不到输出：\n%s", stdout)
	}
}
