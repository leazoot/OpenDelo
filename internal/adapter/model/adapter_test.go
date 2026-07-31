package model_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/model"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 模型 Adapter 的行为用例（REQ-ADAPTER-004）。
 *
 * 全部对本地假服务发起，**不调用任何真实模型服务**
 */

const operationID = "01J0MODELOPERATIONID0000"

// completionInput 是一次合法的推理请求，用例只改自己关心的那一项。
const completionInput = `{"model":"gpt-4o-mini","max_tokens":100,` +
	`"messages":[{"role":"user","content":"hello"}]}`

type fakeModelService struct {
	*httptest.Server
	requests atomic.Int64
	received chan *http.Request
	status   int
	body     string
}

func newFakeService(t *testing.T, status int, body string) *fakeModelService {
	t.Helper()

	fake := &fakeModelService{received: make(chan *http.Request, 4), status: status, body: body}
	fake.Server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, incoming *http.Request) {
			fake.requests.Add(1)
			select {
			case fake.received <- incoming.Clone(incoming.Context()):
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

func newAdapter(t *testing.T, provider model.Provider, baseURL string) *model.Adapter {
	t.Helper()

	adapter, err := model.New(model.Options{Provider: provider, BaseURL: baseURL})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	return adapter
}

func credential(t *testing.T) secret.Value {
	t.Helper()

	value := secret.New([]byte(sentinel.SentinelAPIKey))
	t.Cleanup(value.Zero)
	return value
}

// generousBudget 足够跑完一次用例里的调用。
func generousBudget() model.Budget {
	return model.Budget{MaxCostMicros: 10_000_000, MaxRequests: 10}
}

func assertNoSentinel(t *testing.T, text string) {
	t.Helper()

	for _, value := range sentinel.All() {
		if strings.Contains(text, value) {
			t.Fatalf("输出里出现了哨兵 %s：%s", value, text)
		}
	}
}

// ——— 声明与端点白名单（AC1）———

func TestCapabilities_RegisterWithoutAnyDeclarationError(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderOpenAI, model.ProviderAnthropic} {
		if _, err := registry.New(newAdapter(t, provider, "https://example.com")); err != nil {
			t.Fatalf("%s 注册失败：%v", provider, err)
		}
	}
}

func TestCapabilities_OnlyInferenceAndModelListingAreDeclared(t *testing.T) {
	expected := map[string]registry.RiskLabel{
		"create_completion": registry.RiskLabelMedium,
		"read_models":       registry.RiskLabelLow,
	}

	for _, provider := range []model.Provider{model.ProviderOpenAI, model.ProviderAnthropic} {
		declared := newAdapter(t, provider, "https://example.com").Capabilities()
		if len(declared) != len(expected) {
			t.Fatalf("%s 声明了 %d 项能力，期望 %d 项", provider, len(declared), len(expected))
		}
		for _, capability := range declared {
			want, known := expected[capability.Operation]
			if !known {
				t.Errorf("%s 多出来一项能力：%s", provider, capability.Operation)
				continue
			}
			if capability.RiskLabel != want {
				t.Errorf("%s 的 %s 风险标签为 %s，期望 %s",
					provider, capability.Operation, capability.RiskLabel, want)
			}
		}
	}
}

func TestExecute_BillingMembersKeysAndOrgSettings_AreNotOffered(t *testing.T) {
	// AC1：这四类端点一项都没有被声明 —— 模型服务的凭据往往同时能改计费
	// 与签发新 Key，而 Agent 要的只是推理。
	operations := []string{
		"read_billing", "read_usage_costs",
		"list_members", "invite_member",
		"create_api_key", "delete_api_key", "list_api_keys",
		"read_organization", "update_organization_settings",
	}

	for _, provider := range []model.Provider{model.ProviderOpenAI, model.ProviderAnthropic} {
		for _, operation := range operations {
			t.Run(string(provider)+"/"+operation+"未提供", func(t *testing.T) {
				fake := newFakeService(t, http.StatusOK, `{}`)
				adapter := newAdapter(t, provider, fake.URL)

				_, err := adapter.Execute(t.Context(), model.ExecuteRequest{
					Operation: operation, Budget: generousBudget(),
					Credential: credential(t), OperationID: operationID,
				})
				if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
					t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）",
						apperr.CodeOf(err), err)
				}
				if got := fake.requests.Load(); got != 0 {
					t.Errorf("未提供的能力仍然产生了 %d 次出站请求", got)
				}
			})
		}
	}
}

func TestNew_UnknownProvider_IsRefused(t *testing.T) {
	if _, err := model.New(model.Options{Provider: "gemini"}); !apperr.Is(
		err, apperr.CodeInvalidConfiguration) {
		t.Fatalf("错误码为 %s，期望 invalid_configuration", apperr.CodeOf(err))
	}
}

// ——— 预算（AC3）———

func TestExecute_OverBudget_IsRefusedNotTruncated(t *testing.T) {
	// 截断会让 Agent 拿到一个看似正常、其实缺了一半上下文的答复，
	// 而它没有办法知道。
	cases := []struct {
		name    string
		budget  model.Budget
		spent   model.Usage
		wantErr bool
		// reason 是拒绝理由里必须出现的那句话。缺了它，一条检查被删掉后
		// 用例仍会因为**另一条**检查而通过 —— 那样测的就不是它自己了。
		reason string
	}{
		{"没有给出预算", model.Budget{}, model.Usage{}, true, "没有给出预算上限"},
		{"只给了次数没给费用", model.Budget{MaxRequests: 5}, model.Usage{}, true, "没有给出预算上限"},
		{"费用上限不够", model.Budget{MaxCostMicros: 1, MaxRequests: 5}, model.Usage{}, true, "会超过上限"},
		{"次数已经用完", generousBudget(), model.Usage{Requests: 10}, true, "调用次数"},
		{
			"已花的加上这次会超",
			model.Budget{MaxCostMicros: 100, MaxRequests: 5},
			model.Usage{CostMicros: 99},
			true,
			"会超过上限",
		},
		{"预算够", generousBudget(), model.Usage{}, false, ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeService(t, http.StatusOK, `{"id":"c1","model":"gpt-4o-mini"}`)
			adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

			_, err := adapter.Execute(t.Context(), model.ExecuteRequest{
				Operation: "create_completion", Input: json.RawMessage(completionInput),
				Budget: testCase.budget, Spent: testCase.spent,
				Credential: credential(t), OperationID: operationID,
			})

			if !testCase.wantErr {
				if err != nil {
					t.Fatalf("预算够却被拒绝：%v", err)
				}
				return
			}
			if !apperr.Is(err, apperr.CodeForbidden) {
				t.Fatalf("错误码为 %s，期望 forbidden（%v）", apperr.CodeOf(err), err)
			}
			if !strings.Contains(err.Error(), testCase.reason) {
				t.Errorf("拒绝理由为 %q，期望来自 %q 那条检查", err, testCase.reason)
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("超预算仍然产生了 %d 次出站请求", got)
			}
		})
	}
}

func TestEstimate_ModelNotInThePriceList_IsRefused(t *testing.T) {
	// 估不出费用就等于预算管不住，「算不出来就先跑一次」正是 Fail Closed 要挡的。
	fake := newFakeService(t, http.StatusOK, `{}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	cases := []struct {
		name  string
		input string
	}{
		{"模型不在价目表里", `{"model":"gpt-5-secret","max_tokens":10,"messages":[]}`},
		{"没有给 max_tokens", `{"model":"gpt-4o-mini","messages":[]}`},
		{"max_tokens 为零", `{"model":"gpt-4o-mini","max_tokens":0,"messages":[]}`},
		{"请求体不是 JSON", `{`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝", func(t *testing.T) {
			request := model.ExecuteRequest{
				Operation: "create_completion", Input: json.RawMessage(testCase.input),
				Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
			}
			if _, err := adapter.Estimate(request); !apperr.Is(err, apperr.CodeInvalidRequest) {
				t.Fatalf("估算的错误码为 %s，期望 invalid_request（%v）", apperr.CodeOf(err), err)
			}
			if _, err := adapter.Execute(t.Context(), request); !apperr.Is(
				err, apperr.CodeInvalidRequest) {
				t.Fatalf("执行的错误码为 %s，期望 invalid_request", apperr.CodeOf(err))
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("估不出费用仍然产生了 %d 次出站请求", got)
			}
		})
	}
}

func TestEstimate_CostRoundsUp(t *testing.T) {
	// 预算是上限，算少了就等于放行了一次本该拒绝的调用。
	adapter := newAdapter(t, model.ProviderOpenAI, "https://example.com")

	usage, err := adapter.Estimate(model.ExecuteRequest{
		Operation: "create_completion",
		Input:     json.RawMessage(`{"model":"gpt-4o-mini","max_tokens":1,"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("估算失败：%v", err)
	}
	// 输出单价 600 微元/千 token，一个 token 是 0.6 微元 —— 向上取整为 1。
	if usage.CostMicros != 1 {
		t.Errorf("一个 token 的费用估为 %d 微元，期望向上取整为 1", usage.CostMicros)
	}
	if !usage.Estimated {
		t.Error("估算结果没有标记为估算值")
	}
}

// ——— 用量回报（AC2）———

func TestExecute_ReportedUsage_ReplacesTheEstimate(t *testing.T) {
	// 调用次数与费用要写进 Lease 与审计，厂商回报了实际值就用实际值。
	cases := []struct {
		name         string
		provider     model.Provider
		body         string
		wantInput    int
		wantOutput   int
		wantEstimate bool
	}{
		{
			"OpenAI 回报 prompt/completion", model.ProviderOpenAI,
			`{"id":"c1","model":"gpt-4o-mini","usage":{"prompt_tokens":20,"completion_tokens":30}}`,
			20, 30, false,
		},
		{
			"Anthropic 回报 input/output", model.ProviderAnthropic,
			`{"id":"m1","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":11,"output_tokens":22}}`,
			11, 22, false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeService(t, http.StatusOK, testCase.body)
			adapter := newAdapter(t, testCase.provider, fake.URL)

			input := completionInput
			if testCase.provider == model.ProviderAnthropic {
				input = `{"model":"claude-haiku-4-5-20251001","max_tokens":100,` +
					`"messages":[{"role":"user","content":"hello"}]}`
			}

			executed, err := adapter.Execute(t.Context(), model.ExecuteRequest{
				Operation: "create_completion", Input: json.RawMessage(input),
				Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
			})
			if err != nil {
				t.Fatalf("执行失败：%v", err)
			}
			if !executed.Result.OK {
				t.Fatalf("结果为失败：%+v", executed.Result.Error)
			}

			if executed.Usage.InputTokens != testCase.wantInput ||
				executed.Usage.OutputTokens != testCase.wantOutput {
				t.Errorf("用量为 %d/%d，期望 %d/%d",
					executed.Usage.InputTokens, executed.Usage.OutputTokens,
					testCase.wantInput, testCase.wantOutput)
			}
			if executed.Usage.Estimated != testCase.wantEstimate {
				t.Errorf("Estimated 为 %v，期望 %v", executed.Usage.Estimated, testCase.wantEstimate)
			}
			if executed.Usage.Requests != 1 || executed.Usage.CostMicros <= 0 {
				t.Errorf("次数或费用不对：%+v", executed.Usage)
			}
		})
	}
}

func TestExecute_WhenUsageIsNotReported_TheEstimateIsKept(t *testing.T) {
	// 少算了的用量会让预算在下一次调用时管不住。
	fake := newFakeService(t, http.StatusOK, `{"id":"c1","model":"gpt-4o-mini"}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	executed, err := adapter.Execute(t.Context(), model.ExecuteRequest{
		Operation: "create_completion", Input: json.RawMessage(completionInput),
		Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !executed.Usage.Estimated {
		t.Error("厂商没有回报用量，结果却没有标记为估算值")
	}
	if executed.Usage.OutputTokens != 100 {
		t.Errorf("输出 token 为 %d，期望沿用估算的 max_tokens", executed.Usage.OutputTokens)
	}
}

func TestExecute_FailedCallStillReportsUsage(t *testing.T) {
	// 失败的调用照样可能被计费，用量不能因为失败就不记。
	fake := newFakeService(t, http.StatusInternalServerError, `{}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	executed, err := adapter.Execute(t.Context(), model.ExecuteRequest{
		Operation: "create_completion", Input: json.RawMessage(completionInput),
		Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行返回了传输层错误：%v", err)
	}
	if executed.Result.OK {
		t.Fatal("上游 500 被当成了成功")
	}
	if executed.Usage.Requests != 1 {
		t.Errorf("失败的调用报告了 %d 次请求，期望 1", executed.Usage.Requests)
	}
}

// ——— 调用与脱敏 ———

func TestExecute_CredentialGoesIntoTheProviderSpecificHeader(t *testing.T) {
	cases := []struct {
		provider model.Provider
		header   string
		value    string
		path     string
	}{
		{model.ProviderOpenAI, "Authorization", "Bearer " + sentinel.SentinelAPIKey, "/chat/completions"},
		{model.ProviderAnthropic, "x-api-key", sentinel.SentinelAPIKey, "/messages"},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.provider)+"的凭据注入", func(t *testing.T) {
			fake := newFakeService(t, http.StatusOK, `{"id":"c1"}`)
			adapter := newAdapter(t, testCase.provider, fake.URL)

			input := completionInput
			if testCase.provider == model.ProviderAnthropic {
				input = `{"model":"claude-haiku-4-5-20251001","max_tokens":10,"messages":[]}`
			}

			if _, err := adapter.Execute(t.Context(), model.ExecuteRequest{
				Operation: "create_completion", Input: json.RawMessage(input),
				Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
			}); err != nil {
				t.Fatalf("执行失败：%v", err)
			}

			incoming := <-fake.received
			if got := incoming.Header.Get(testCase.header); got != testCase.value {
				t.Errorf("请求头 %s 为 %q", testCase.header, got)
			}
			if incoming.URL.Path != testCase.path {
				t.Errorf("请求路径为 %q，期望 %q", incoming.URL.Path, testCase.path)
			}
			if strings.Contains(incoming.URL.String(), sentinel.SentinelAPIKey) {
				t.Error("凭据出现在了 URL 里")
			}
		})
	}
}

func TestExecute_ResponseIsFilteredAndRedacted(t *testing.T) {
	body := `{"id":"c1","model":"gpt-4o-mini","choices":[{"message":{"content":"hi"}}],` +
		`"api_key":"` + sentinel.SentinelAPIKey + `","organization_id":"org-secret-1"}`

	fake := newFakeService(t, http.StatusOK, body)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	executed, err := adapter.Execute(t.Context(), model.ExecuteRequest{
		Operation: "create_completion", Input: json.RawMessage(completionInput),
		Budget: generousBudget(), Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	encoded, err := json.Marshal(executed.Result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))
	if strings.Contains(string(encoded), "organization_id") {
		t.Error("白名单之外的字段被返回了")
	}
	if strings.Contains(string(encoded), `"headers"`) {
		t.Error("返回体里出现了 headers 字段")
	}
	if !strings.Contains(string(executed.Result.Data), `"id":"c1"`) {
		t.Errorf("白名单允许的字段没有返回：%s", executed.Result.Data)
	}
}

func TestExecute_ReadModels_IsCallable(t *testing.T) {
	fake := newFakeService(t, http.StatusOK,
		`{"object":"list","data":[{"id":"gpt-4o-mini","owned_by":"openai"}],"secret_key":"x"}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	executed, err := adapter.Execute(t.Context(), model.ExecuteRequest{
		Operation: "read_models", Budget: generousBudget(),
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !executed.Result.OK {
		t.Fatalf("结果为失败：%+v", executed.Result.Error)
	}
	if strings.Contains(string(executed.Result.Data), "secret_key") {
		t.Error("白名单之外的字段被返回了")
	}

	if incoming := <-fake.received; incoming.Method != http.MethodGet ||
		incoming.URL.Path != "/models" {
		t.Errorf("假服务收到 %s %s", incoming.Method, incoming.URL.Path)
	}
}

// ——— 无 SDK 依赖（AC4）———

func TestNoVendorSDKDependency(t *testing.T) {
	// PRD §18.3 与技术栈都禁止引入厂商 SDK：
	// SDK 会带来一条不经本包的出站路径，端点白名单也就管不住了。
	content, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatalf("读 go.mod 失败：%v", err)
	}

	banned := []string{
		"github.com/sashabaranov/go-openai",
		"github.com/openai/openai-go",
		"github.com/anthropics/anthropic-sdk-go",
		"github.com/liushuangls/go-anthropic",
	}
	for _, module := range banned {
		if strings.Contains(string(content), module) {
			t.Errorf("go.mod 里出现了厂商 SDK：%s", module)
		}
	}
}
