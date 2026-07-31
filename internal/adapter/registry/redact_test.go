package registry_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 结果脱敏的行为用例（REQ-ADAPTER-007）。
 *
 * AC1 用哨兵响应夹具证明返回体不含凭据；AC2 证明返回体里没有 headers 字段；
 * AC3 证明错误经脱敏后仍保留操作 ID 与可读原因。
 */

// sentinelBody 是一份把哨兵塞满各处的响应夹具。
func sentinelBody() []byte {
	return []byte(`{
		"id": 7,
		"name": "hello",
		"authorization": "Bearer ` + sentinel.SentinelToken + `",
		"headers": {"Set-Cookie": "session=` + sentinel.SentinelToken + `"},
		"owner": {
			"login": "octocat",
			"access_token": "` + sentinel.SentinelToken + `",
			"api_key": "` + sentinel.SentinelAPIKey + `",
			"private_key": "` + sentinel.SentinelPrivateKey + `"
		},
		"collaborators": [{"password": "` + sentinel.SentinelPassword + `"}],
		"url": "https://api.example.com/x?token=` + sentinel.SentinelToken + `"
	}`)
}

func redactingCapability() registry.Capability {
	capability := readCapability()
	capability.ResponseFields = []string{"id", "name", "owner", "collaborators", "url", "secrets"}
	return capability
}

func assertNoSentinel(t *testing.T, text string) {
	t.Helper()

	for _, value := range sentinel.All() {
		if strings.Contains(text, value) {
			t.Fatalf("输出里出现了哨兵 %s：%s", value, text)
		}
	}
}

func TestRedact_SentinelFixture_LeavesNoCredentialInTheBody(t *testing.T) {
	// AC1：返回体中不含哨兵字符串。
	redacted, err := registry.Redact(sentinelBody(), redactingCapability())
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	assertNoSentinel(t, string(redacted))
}

func TestRedact_ResultHasNoHeadersField(t *testing.T) {
	// AC2：headers 不是被删掉的，而是在类型上就不存在。
	redacted, err := registry.Redact(sentinelBody(), redactingCapability())
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}

	encoded, err := json.Marshal(registry.Success(operationID, redacted, nil))
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))

	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if _, present := decoded["headers"]; present {
		t.Fatal("返回体里出现了 headers 字段")
	}

	var data map[string]json.RawMessage
	if err = json.Unmarshal(decoded["data"], &data); err != nil {
		t.Fatalf("反序列化 data 失败：%v", err)
	}
	if _, present := data["headers"]; present {
		t.Fatal("data 里出现了 headers 字段")
	}
}

func TestRedact_FieldsOutsideTheAllowlist_AreDropped(t *testing.T) {
	// 白名单在前：外部服务将来新增的字段默认出不去，不必有人记得来加规则。
	redacted, err := registry.Redact(sentinelBody(), redactingCapability())
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}

	var data map[string]any
	if err = json.Unmarshal(redacted, &data); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}

	for _, dropped := range []string{"authorization", "headers"} {
		if _, present := data[dropped]; present {
			t.Errorf("白名单之外的 %s 仍然被返回", dropped)
		}
	}
	for _, kept := range []string{"id", "name", "owner"} {
		if _, present := data[kept]; !present {
			t.Errorf("白名单里的 %s 没有被返回", kept)
		}
	}
}

func TestRedact_NestedSensitiveKeys_AreReplacedNotDropped(t *testing.T) {
	// 抹掉值而不是删掉键：读的人仍然知道这里原本有个字段。
	redacted, err := registry.Redact(sentinelBody(), redactingCapability())
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}

	var data struct {
		Owner struct {
			Login       string `json:"login"`
			AccessToken string `json:"access_token"`
			APIKey      string `json:"api_key"`
			PrivateKey  string `json:"private_key"`
		} `json:"owner"`
		Collaborators []struct {
			Password string `json:"password"`
		} `json:"collaborators"`
	}
	if err = json.Unmarshal(redacted, &data); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}

	if data.Owner.Login != "octocat" {
		t.Errorf("无关字段被改成了 %q", data.Owner.Login)
	}
	for name, value := range map[string]string{
		"access_token": data.Owner.AccessToken,
		"api_key":      data.Owner.APIKey,
		"private_key":  data.Owner.PrivateKey,
	} {
		if value != registry.RedactedMarker {
			t.Errorf("%s 为 %q，期望 %s", name, value, registry.RedactedMarker)
		}
	}
	if len(data.Collaborators) != 1 || data.Collaborators[0].Password != registry.RedactedMarker {
		t.Errorf("数组里的 password 没有被抹掉：%+v", data.Collaborators)
	}
}

func TestRedact_URLQueryCarryingACredential_LosesTheQuery(t *testing.T) {
	capability := readCapability()
	capability.ResponseFields = []string{"url", "name"}

	body := []byte(`{
		"url": "https://api.example.com/x?token=` + sentinel.SentinelToken + `&page=2",
		"name": "https://api.example.com/y?page=2"
	}`)

	redacted, err := registry.Redact(body, capability)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	assertNoSentinel(t, string(redacted))

	var data struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err = json.Unmarshal(redacted, &data); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if data.URL != "https://api.example.com/x" {
		t.Errorf("带凭据的 URL 变成了 %q，期望只留 path", data.URL)
	}
	// 无差别砍掉 query 会让外部服务返回的资源地址变得不可用。
	if data.Name != "https://api.example.com/y?page=2" {
		t.Errorf("不含凭据的 URL 被改成了 %q", data.Name)
	}
}

func TestRedact_AdapterDeclaredRules_AreAppliedOnTopOfTheGlobalList(t *testing.T) {
	capability := readCapability()
	capability.ResponseFields = []string{"webhook", "name"}
	capability.RedactionRules = []string{"webhook"}

	body := []byte(`{"webhook":"` + sentinel.SentinelToken + `","name":"hello"}`)

	redacted, err := registry.Redact(body, capability)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	assertNoSentinel(t, string(redacted))
	if !strings.Contains(string(redacted), `"name":"hello"`) {
		t.Errorf("无关字段被一起抹掉了：%s", redacted)
	}
}

func TestRedact_ArrayResponses_AreFilteredElementByElement(t *testing.T) {
	capability := readCapability()
	capability.ResponseFields = []string{"id"}

	body := []byte(`[{"id":1,"token":"` + sentinel.SentinelToken +
		`"},{"id":2,"password":"` + sentinel.SentinelPassword + `"}]`)

	redacted, err := registry.Redact(body, capability)
	if err != nil {
		t.Fatalf("脱敏失败：%v", err)
	}
	assertNoSentinel(t, string(redacted))
	if string(redacted) != `[{"id":1},{"id":2}]` {
		t.Errorf("过滤结果为 %s", redacted)
	}
}

func TestRedact_ResponsesThatCannotBeFiltered_AreRefused(t *testing.T) {
	// 过滤不了就不返回：把一份没过滤过的响应交给 Agent 比返回错误糟得多。
	capability := redactingCapability()

	cases := []struct {
		name string
		body string
	}{
		{"不是合法 JSON", `{"id":`},
		{"顶层是字符串", `"` + sentinel.SentinelToken + `"`},
		{"顶层是数字", `7`},
		{"数组里不是对象", `[1,2]`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝返回", func(t *testing.T) {
			redacted, err := registry.Redact([]byte(testCase.body), capability)
			if !apperr.Is(err, apperr.CodeInternal) {
				t.Fatalf("错误码为 %s，期望 internal（%v）", apperr.CodeOf(err), err)
			}
			if redacted != nil {
				t.Errorf("拒绝时仍然返回了内容：%s", redacted)
			}
		})
	}
}

func TestRedact_EmptyBody_ReturnsNothing(t *testing.T) {
	redacted, err := registry.Redact(nil, redactingCapability())
	if err != nil {
		t.Fatalf("空响应被当成错误：%v", err)
	}
	if redacted != nil {
		t.Errorf("空响应返回了 %s", redacted)
	}
}

// ——— 错误脱敏（AC3）———

func TestFailure_KeepsTheOperationIDAndAReadableReason(t *testing.T) {
	cause := apperr.New(apperr.CodeAdapterTimeout).
		WithDetail("外部服务返回了 " + sentinel.SentinelToken)

	result := registry.Failure(operationID, cause)

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))

	if result.OK {
		t.Error("失败结果的 OK 为真")
	}
	if result.OperationID != operationID {
		t.Errorf("操作 ID 为 %q，期望 %q", result.OperationID, operationID)
	}
	if result.Error == nil || result.Error.Code != "adapter_timeout" {
		t.Fatalf("错误为 %+v，期望保留 adapter_timeout", result.Error)
	}
	if strings.TrimSpace(result.Error.Message) == "" {
		t.Error("错误没有可读原因")
	}
}

func TestFailure_ErrorWithoutACode_BecomesInternal(t *testing.T) {
	result := registry.Failure(operationID, errPlain{})

	if result.Error == nil || result.Error.Code != "internal" {
		t.Fatalf("错误为 %+v，期望规整为 internal", result.Error)
	}
	if result.OperationID != operationID {
		t.Errorf("操作 ID 为 %q", result.OperationID)
	}
}

type errPlain struct{}

func (errPlain) Error() string { return "外部服务返回了 " + sentinel.SentinelToken }

// ——— 无结构文本 ———

func TestRedactText_MasksValuesOfSensitiveNamesAndKeepsTheRest(t *testing.T) {
	log := "Authorization: Bearer " + sentinel.SentinelToken + "\n" +
		"api_key=" + sentinel.SentinelAPIKey + "\n" +
		"branch=main\n" +
		"deploy_secret: \"" + sentinel.SentinelPassword + "\"\n"

	cleaned := registry.RedactText(log, []string{"webhook"})

	assertNoSentinel(t, cleaned)
	if !strings.Contains(cleaned, "branch=main") {
		t.Errorf("无关内容被改掉了：%s", cleaned)
	}
	// 保留字段名：整行删掉会让日志读起来像是缺了一段。
	for _, name := range []string{"Authorization", "api_key", "deploy_secret"} {
		if !strings.Contains(cleaned, name) {
			t.Errorf("字段名 %s 被一起删掉了：%s", name, cleaned)
		}
	}
}

func TestRedactText_AdapterDeclaredRules_AreApplied(t *testing.T) {
	cleaned := registry.RedactText("webhook=https://hooks.example.com/"+
		sentinel.SentinelToken, []string{"webhook"})

	assertNoSentinel(t, cleaned)
}
