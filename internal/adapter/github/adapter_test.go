package github_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * GitHub Adapter 的行为用例（REQ-ADAPTER-002）。
 *
 * 全部对本地假服务发起，**不访问真实的 GitHub**
 */

const operationID = "01J0GITHUBOPERATIONID000"

type fakeGitHub struct {
	*httptest.Server
	requests  atomic.Int64
	requested chan requested
	status    int
	body      string
}

type requested struct {
	method string
	path   string
	auth   string
	body   string
}

func newFakeGitHub(t *testing.T, status int, body string) *fakeGitHub {
	t.Helper()

	fake := &fakeGitHub{requested: make(chan requested, 8), status: status, body: body}
	fake.Server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, incoming *http.Request) {
			fake.requests.Add(1)
			payload, readErr := io.ReadAll(incoming.Body)
			if readErr != nil {
				panic(readErr)
			}
			select {
			case fake.requested <- requested{
				method: incoming.Method,
				path:   incoming.URL.RequestURI(),
				auth:   incoming.Header.Get("Authorization"),
				body:   string(payload),
			}:
			default:
			}
			writer.WriteHeader(fake.status)
			if _, err := io.WriteString(writer, fake.body); err != nil {
				panic(err)
			}
		}))
	t.Cleanup(fake.Close)
	return fake
}

func newAdapter(t *testing.T, baseURL string) *github.Adapter {
	t.Helper()

	adapter, err := github.New(github.Options{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	return adapter
}

func repositoryResource() map[string]string {
	return map[string]string{
		"owner": "octocat", "repo": "hello",
		"number": "7", "run_id": "42",
		"username": "someone", "secret_name": "DEPLOY_KEY",
	}
}

func assertNoSentinel(t *testing.T, text string) {
	t.Helper()

	for _, value := range sentinel.All() {
		if strings.Contains(text, value) {
			t.Fatalf("输出里出现了哨兵 %s：%s", value, text)
		}
	}
}

// ——— 声明（AC2、REQ-ADAPTER-001 AC3）———

func TestCapabilities_RegisterWithoutAnyDeclarationError(t *testing.T) {
	// 十三项声明全部要通过注册表的九项校验与一致性校验。
	if _, err := registry.New(newAdapter(t, "https://example.com")); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
}

func TestCapabilities_RiskLabelsAreDeclaredOneByOne(t *testing.T) {
	// 表驱动逐条断言风险标签（REQ-ADAPTER-001 AC3、REQ-ADAPTER-002 AC2）。
	expected := map[string]registry.RiskLabel{
		"read_repository":            registry.RiskLabelLow,
		"read_issue":                 registry.RiskLabelLow,
		"read_pull_request":          registry.RiskLabelLow,
		"read_actions_run":           registry.RiskLabelLow,
		"read_actions_logs":          registry.RiskLabelLow,
		"create_issue":               registry.RiskLabelMedium,
		"create_pull_request":        registry.RiskLabelMedium,
		"create_comment":             registry.RiskLabelMedium,
		"create_branch":              registry.RiskLabelMedium,
		"merge_default_branch":       registry.RiskLabelHigh,
		"delete_repository":          registry.RiskLabelHigh,
		"update_collaborator":        registry.RiskLabelHigh,
		"update_secret":              registry.RiskLabelHigh,
		"update_actions_permissions": registry.RiskLabelHigh,
	}

	declared := newAdapter(t, "https://example.com").Capabilities()
	if len(declared) != len(expected) {
		t.Fatalf("声明了 %d 项能力，期望 %d 项（9 个可执行操作 + 5 项高风险）",
			len(declared), len(expected))
	}

	for _, capability := range declared {
		want, known := expected[capability.Operation]
		if !known {
			t.Errorf("多出来一项没有预期的能力：%s", capability.Operation)
			continue
		}
		if capability.RiskLabel != want {
			t.Errorf("%s 的风险标签为 %s，期望 %s", capability.Operation, capability.RiskLabel, want)
		}
		delete(expected, capability.Operation)
	}
	for operation := range expected {
		t.Errorf("少了一项能力：%s", operation)
	}
}

func TestCapabilities_TheFiveHighRiskOnes_DeclareWhyTheyAreHigh(t *testing.T) {
	// 光标成 high 不够：风险等级要能被解释，而解释来自操作性质与回滚能力。
	expected := map[string]func(registry.Capability) bool{
		"delete_repository": func(c registry.Capability) bool {
			return c.Nature.Destructive && c.Rollback == registry.RollbackNone
		},
		"update_collaborator": func(c registry.Capability) bool { return c.Nature.PermissionChange },
		"update_actions_permissions": func(c registry.Capability) bool {
			return c.Nature.PermissionChange
		},
		"update_secret":        func(c registry.Capability) bool { return c.Nature.SecretAccess },
		"merge_default_branch": func(c registry.Capability) bool { return c.Rollback == registry.RollbackNone },
	}

	for _, capability := range newAdapter(t, "https://example.com").Capabilities() {
		check, watched := expected[capability.Operation]
		if !watched {
			continue
		}
		if !check(capability) {
			t.Errorf("%s 没有声明使它成为高风险的那项性质：%+v", capability.Operation, capability)
		}
		delete(expected, capability.Operation)
	}
	for operation := range expected {
		t.Errorf("高风险操作 %s 不在声明里", operation)
	}
}

func TestCapabilities_CreateComment_IsDeclaredAsExternalCommunication(t *testing.T) {
	// 评论会发到别人的通知里，收回不了那一份。这项性质会把写操作的风险
	// 抬到至少 medium（PRD §10.5），漏声明就等于把它当成一次普通写入。
	for _, capability := range newAdapter(t, "https://example.com").Capabilities() {
		if capability.Operation != "create_comment" {
			continue
		}
		if !capability.Nature.ExternalCommunication {
			t.Fatal("create_comment 没有声明对外通信")
		}
		return
	}
	t.Fatal("声明里没有 create_comment")
}

// ——— 八项 MVP 能力（AC1）———

func TestExecute_AllEightMVPCapabilities_AreCallableAndRedacted(t *testing.T) {
	// 每个响应里都塞进哨兵与一个白名单外的字段。
	body := `{"id":1,"number":7,"ref":"refs/heads/x","token":"` + sentinel.SentinelToken +
		`","authorization":"Bearer ` + sentinel.SentinelToken + `","internal_note":"leak"}`

	cases := []struct {
		operation string
		method    string
		path      string
		input     string
	}{
		{"read_repository", "GET", "/repos/octocat/hello", ""},
		{"read_issue", "GET", "/repos/octocat/hello/issues/7", ""},
		{"read_pull_request", "GET", "/repos/octocat/hello/pulls/7", ""},
		{"read_actions_run", "GET", "/repos/octocat/hello/actions/runs/42", ""},
		{"create_issue", "POST", "/repos/octocat/hello/issues", `{"title":"hi"}`},
		{"create_pull_request", "POST", "/repos/octocat/hello/pulls", `{"title":"hi","head":"a","base":"b"}`},
		{"create_comment", "POST", "/repos/octocat/hello/issues/7/comments", `{"body":"hi"}`},
		{"create_branch", "POST", "/repos/octocat/hello/git/refs", `{"ref":"refs/heads/x","sha":"abc"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.operation+"可调用且结果已脱敏", func(t *testing.T) {
			fake := newFakeGitHub(t, http.StatusOK, body)
			adapter := newAdapter(t, fake.URL)

			credential := secret.New([]byte(sentinel.SentinelToken))
			defer credential.Zero()

			result, err := adapter.Execute(t.Context(), github.ExecuteRequest{
				Operation:   testCase.operation,
				Resource:    repositoryResource(),
				Input:       json.RawMessage(testCase.input),
				Credential:  credential,
				OperationID: operationID,
			})
			if err != nil {
				t.Fatalf("执行失败：%v", err)
			}
			if !result.OK {
				t.Fatalf("结果为失败：%+v", result.Error)
			}

			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("序列化失败：%v", err)
			}
			assertNoSentinel(t, string(encoded))
			if strings.Contains(string(encoded), "internal_note") {
				t.Error("白名单之外的字段被返回了")
			}
			if strings.Contains(string(encoded), `"headers"`) {
				t.Error("返回体里出现了 headers 字段")
			}

			got := <-fake.requested
			if got.method != testCase.method || got.path != testCase.path {
				t.Errorf("假服务收到 %s %s，期望 %s %s",
					got.method, got.path, testCase.method, testCase.path)
			}
			if got.auth != "Bearer "+sentinel.SentinelToken {
				t.Errorf("凭据没有注入到 Authorization：%q", got.auth)
			}
			if testCase.input != "" && got.body != testCase.input {
				t.Errorf("请求体为 %q，期望 %q", got.body, testCase.input)
			}
		})
	}
}

func TestExecute_ReadActionsRunAndLogs_AreSeparateOperations(t *testing.T) {
	// 「读取 Actions」一个能力域下两个操作：只读日志的话 Agent 问不出
	// 「CI 过了没有」，只读运行状态的话日志脱敏就没有落点。
	fake := newFakeGitHub(t, http.StatusOK,
		`{"id":42,"status":"completed","conclusion":"success","internal_note":"leak"}`)
	adapter := newAdapter(t, fake.URL)

	result, err := adapter.Execute(t.Context(), github.ExecuteRequest{
		Operation: "read_actions_run", Resource: repositoryResource(), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	var data struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err = json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if data.Status != "completed" || data.Conclusion != "success" {
		t.Errorf("运行结论为 %+v，期望 Agent 能读到 CI 过没过", data)
	}
	if strings.Contains(string(result.Data), "internal_note") {
		t.Error("白名单之外的字段被返回了")
	}

	if got := <-fake.requested; got.path != "/repos/octocat/hello/actions/runs/42" {
		t.Errorf("假服务收到的是 %q，期望运行状态端点而不是日志端点", got.path)
	}
}

func TestExecute_ReadActionsLogs_AppliesLocalRedactionOnTopOfGitHubMasking(t *testing.T) {
	// AC3：GitHub 的掩码只认得它自己知道的 Secret。一个从别处粘进构建脚本、
	// 被 echo 出来的令牌不会被它掩掉。
	log := "2026-07-28 build start\n" +
		"##[group]Run deploy\n" +
		"GITHUB_TOKEN: ***\n" +
		"leaked=ghp_" + strings.Repeat("A", 36) + "\n" +
		"api_key=" + sentinel.SentinelAPIKey + "\n" +
		"webhook: https://hooks.example.com/" + sentinel.SentinelToken + "\n" +
		"branch=main\n"

	fake := newFakeGitHub(t, http.StatusOK, log)
	adapter := newAdapter(t, fake.URL)

	result, err := adapter.Execute(t.Context(), github.ExecuteRequest{
		Operation:   "read_actions_logs",
		Resource:    repositoryResource(),
		OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))
	if strings.Contains(string(encoded), "ghp_") {
		t.Errorf("裸的 GitHub 令牌没有被本地规则抹掉：%s", encoded)
	}

	var data struct {
		Logs string `json:"logs"`
	}
	if err = json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	// 日志还要能读：无关内容不能被一起抹掉。
	for _, kept := range []string{"build start", "branch=main", "GITHUB_TOKEN"} {
		if !strings.Contains(data.Logs, kept) {
			t.Errorf("无关内容 %q 被抹掉了：%s", kept, data.Logs)
		}
	}

	if got := <-fake.requested; got.path != "/repos/octocat/hello/actions/runs/42/logs" {
		t.Errorf("假服务收到的是 %q", got.path)
	}
}

func TestRedactActionsLog_MasksEveryGitHubTokenShape(t *testing.T) {
	shapes := []string{
		"ghp_" + strings.Repeat("A", 36),
		"gho_" + strings.Repeat("B", 36),
		"ghu_" + strings.Repeat("C", 36),
		"ghs_" + strings.Repeat("D", 36),
		"ghr_" + strings.Repeat("E", 36),
		"github_pat_" + strings.Repeat("F", 22),
	}

	for _, shape := range shapes {
		cleaned := github.RedactActionsLog("echo "+shape+" done", nil)
		if strings.Contains(cleaned, shape) {
			t.Errorf("令牌形状 %s 没有被抹掉：%s", shape[:8], cleaned)
		}
		if !strings.Contains(cleaned, "done") {
			t.Errorf("无关内容被一起抹掉了：%s", cleaned)
		}
	}
}

// ——— 五项高风险只声明不执行 ———

func TestExecute_TheFiveHighRiskOperations_AreDeclaredButNotImplemented(t *testing.T) {
	// 返回未实现错误而不是悄悄成功：一个「看起来做了」的合并主分支
	// 比明确的失败危险得多。
	operations := []string{
		"merge_default_branch", "delete_repository", "update_collaborator",
		"update_secret", "update_actions_permissions",
	}

	for _, operation := range operations {
		t.Run(operation+"返回未实现且不发出请求", func(t *testing.T) {
			fake := newFakeGitHub(t, http.StatusOK, `{}`)
			adapter := newAdapter(t, fake.URL)

			_, err := adapter.Execute(t.Context(), github.ExecuteRequest{
				Operation:   operation,
				Resource:    repositoryResource(),
				OperationID: operationID,
			})
			if !apperr.Is(err, apperr.CodeNotImplemented) {
				t.Fatalf("错误码为 %s，期望 not_implemented（%v）", apperr.CodeOf(err), err)
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("未实现的操作仍然产生了 %d 次出站请求", got)
			}
		})
	}
}

// ——— 输入与失败路径 ———

func TestExecute_UndeclaredOperation_IsCapabilityNotOffered(t *testing.T) {
	fake := newFakeGitHub(t, http.StatusOK, `{}`)
	adapter := newAdapter(t, fake.URL)

	_, err := adapter.Execute(t.Context(), github.ExecuteRequest{
		Operation: "fork_repository", Resource: repositoryResource(), OperationID: operationID,
	})
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered", apperr.CodeOf(err))
	}
	if got := fake.requests.Load(); got != 0 {
		t.Errorf("未声明的操作产生了 %d 次出站请求", got)
	}
}

func TestExecute_ResourceValueThatWouldChangeTheEndpoint_IsRefused(t *testing.T) {
	// 一个带斜杠的 owner 能把 /repos/{owner}/{repo} 变成另一个端点，
	// 而那个端点没有被声明过。
	cases := []struct {
		name  string
		owner string
	}{
		{"取值为空", ""},
		{"取值只有空白", "   "},
		{"取值带斜杠", "octocat/../orgs"},
		{"取值带问号", "octocat?x=1"},
		{"取值带井号", "octocat#frag"},
		{"取值想跳出去", "..."},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝且不发出请求", func(t *testing.T) {
			fake := newFakeGitHub(t, http.StatusOK, `{}`)
			adapter := newAdapter(t, fake.URL)

			resource := repositoryResource()
			resource["owner"] = testCase.owner

			_, err := adapter.Execute(t.Context(), github.ExecuteRequest{
				Operation: "read_repository", Resource: resource, OperationID: operationID,
			})
			if !apperr.Is(err, apperr.CodeInvalidRequest) {
				t.Fatalf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("被拒绝的资源取值仍然产生了 %d 次出站请求", got)
			}
		})
	}
}

func TestExecute_MissingResourceDimension_IsRefused(t *testing.T) {
	fake := newFakeGitHub(t, http.StatusOK, `{}`)
	adapter := newAdapter(t, fake.URL)

	_, err := adapter.Execute(t.Context(), github.ExecuteRequest{
		Operation:   "read_issue",
		Resource:    map[string]string{"owner": "octocat", "repo": "hello"},
		OperationID: operationID,
	})
	if !apperr.Is(err, apperr.CodeInvalidRequest) {
		t.Fatalf("错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
	}
	if got := fake.requests.Load(); got != 0 {
		t.Errorf("缺资源维度时仍然产生了 %d 次出站请求", got)
	}
}

func TestExecute_UpstreamFailure_IsExplainedWithoutTheRawBody(t *testing.T) {
	// 外部服务的原始报文可能回显请求内容（REQ-ADAPTER-007 AC3）。
	cases := []struct {
		status int
		want   apperr.Code
	}{
		{http.StatusUnauthorized, apperr.CodeCredentialNotAuthorized},
		{http.StatusForbidden, apperr.CodeCredentialNotAuthorized},
		{http.StatusNotFound, apperr.CodeNotFound},
		{http.StatusConflict, apperr.CodeConflict},
		{http.StatusUnprocessableEntity, apperr.CodeInvalidRequest},
		{http.StatusBadGateway, apperr.CodeGatewayUnavailable},
	}

	for _, testCase := range cases {
		fake := newFakeGitHub(t, testCase.status,
			`{"message":"`+sentinel.SentinelToken+`"}`)
		adapter := newAdapter(t, fake.URL)

		result, err := adapter.Execute(t.Context(), github.ExecuteRequest{
			Operation: "read_repository", Resource: repositoryResource(), OperationID: operationID,
		})
		if err != nil {
			t.Fatalf("%d 返回了传输层错误：%v", testCase.status, err)
		}
		if result.OK {
			t.Fatalf("%d 被当成了成功", testCase.status)
		}
		if result.Error == nil || result.Error.Code != testCase.want.String() {
			t.Errorf("%d 的错误码为 %+v，期望 %s", testCase.status, result.Error, testCase.want)
		}
		if result.OperationID != operationID {
			t.Errorf("%d 的结果丢了操作 ID", testCase.status)
		}

		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatalf("序列化失败：%v", marshalErr)
		}
		assertNoSentinel(t, string(encoded))
	}
}

func TestExecute_ResponseThatCannotBeFiltered_DoesNotReachTheAgent(t *testing.T) {
	// 过滤不了就不返回：把一份没过滤过的响应交给 Agent 比返回错误糟得多。
	fake := newFakeGitHub(t, http.StatusOK, `"`+sentinel.SentinelToken+`"`)
	adapter := newAdapter(t, fake.URL)

	result, err := adapter.Execute(t.Context(), github.ExecuteRequest{
		Operation: "read_repository", Resource: repositoryResource(), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if result.OK {
		t.Fatal("过滤不了的响应被当成了成功")
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))
}

func TestNew_DefaultsToTheGitHubAPI(t *testing.T) {
	if github.DefaultBaseURL != "https://api.github.com" {
		t.Errorf("默认地址为 %q", github.DefaultBaseURL)
	}
	adapter := newAdapter(t, "")
	if adapter.Service() != "github" || adapter.Kind() != registry.KindGitHub {
		t.Errorf("服务名为 %q，种类为 %q", adapter.Service(), adapter.Kind())
	}
}
