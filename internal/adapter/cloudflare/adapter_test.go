package cloudflare_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * Cloudflare Adapter 的行为用例（REQ-ADAPTER-003）。
 *
 * 全部对本地假服务发起，**不碰任何真实 DNS 记录**
 */

const operationID = "01J0CLOUDFLAREOPERATION0"

// currentRecord 是假服务对单条查询的答复：改动前的当前值。
const currentRecord = `{"success":true,"result":{"id":"rec1","type":"A",` +
	`"name":"www.example.com","content":"203.0.113.10","ttl":300,"proxied":false,` +
	`"zone_id":"zone1","meta":{"api_token":"` + sentinel.SentinelToken + `"}}}`

type exchange struct {
	method string
	path   string
	auth   string
	body   string
}

// fakeCloudflare 按路径给出不同答复，并记下收到的每一次请求。
type fakeCloudflare struct {
	*httptest.Server
	requests  atomic.Int64
	exchanges chan exchange
	status    int
	body      string
}

func newFakeCloudflare(t *testing.T, status int, body string) *fakeCloudflare {
	t.Helper()

	fake := &fakeCloudflare{exchanges: make(chan exchange, 8), status: status, body: body}
	fake.Server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, incoming *http.Request) {
			fake.requests.Add(1)
			payload, err := io.ReadAll(incoming.Body)
			if err != nil {
				panic(err)
			}
			select {
			case fake.exchanges <- exchange{
				method: incoming.Method,
				path:   incoming.URL.RequestURI(),
				auth:   incoming.Header.Get("Authorization"),
				body:   string(payload),
			}:
			default:
			}

			// 单条查询恒返回当前值，其余按用例设定答复。
			if incoming.Method == http.MethodGet &&
				strings.HasSuffix(incoming.URL.Path, "/dns_records/rec1") {
				if _, writeErr := io.WriteString(writer, currentRecord); writeErr != nil {
					panic(writeErr)
				}
				return
			}
			writer.WriteHeader(fake.status)
			if _, writeErr := io.WriteString(writer, fake.body); writeErr != nil {
				panic(writeErr)
			}
		}))
	t.Cleanup(fake.Close)
	return fake
}

func newAdapter(t *testing.T, baseURL string) *cloudflare.Adapter {
	t.Helper()

	adapter, err := cloudflare.New(cloudflare.Options{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	return adapter
}

func resource() map[string]string {
	return map[string]string{
		"zone_id": "zone1", "record_id": "rec1",
		"account_id": "acct1", "tunnel_id": "tun1",
	}
}

func credential(t *testing.T) secret.Value {
	t.Helper()

	value := secret.New([]byte(sentinel.SentinelToken))
	t.Cleanup(value.Zero)
	return value
}

func assertNoSentinel(t *testing.T, text string) {
	t.Helper()

	for _, value := range sentinel.All() {
		if strings.Contains(text, value) {
			t.Fatalf("输出里出现了哨兵 %s：%s", value, text)
		}
	}
}

// ——— 声明 ———

func TestCapabilities_RegisterWithoutAnyDeclarationError(t *testing.T) {
	if _, err := registry.New(newAdapter(t, "https://example.com")); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
}

func TestCapabilities_RiskLabelsAreDeclaredOneByOne(t *testing.T) {
	// 表驱动逐条断言（REQ-ADAPTER-001 AC3）。删除 DNS 是 high 来自 AC2；
	// 大范围修改是 high 来自 AC3。
	expected := map[string]registry.RiskLabel{
		"read_zone":             registry.RiskLabelLow,
		"read_dns_records":      registry.RiskLabelLow,
		"read_dns_record":       registry.RiskLabelLow,
		"read_tunnel":           registry.RiskLabelLow,
		"create_dns_record":     registry.RiskLabelMedium,
		"update_dns_record":     registry.RiskLabelMedium,
		"purge_cache":           registry.RiskLabelMedium,
		"delete_dns_record":     registry.RiskLabelHigh,
		"delete_zone":           registry.RiskLabelHigh,
		"manage_token":          registry.RiskLabelHigh,
		"manage_member":         registry.RiskLabelHigh,
		"bulk_update_dns":       registry.RiskLabelHigh,
		"update_security_rules": registry.RiskLabelHigh,
	}

	declared := newAdapter(t, "https://example.com").Capabilities()
	if len(declared) != len(expected) {
		t.Fatalf("声明了 %d 项能力，期望 %d 项", len(declared), len(expected))
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

func TestCapabilities_DeleteDNSRecord_IsHighAndIrreversibleByDeclaration(t *testing.T) {
	// AC2：删除 DNS 记录是不可逆的，光靠标签不够，性质也要说清楚。
	for _, capability := range newAdapter(t, "https://example.com").Capabilities() {
		if capability.Operation != "delete_dns_record" {
			continue
		}
		if capability.RiskLabel != registry.RiskLabelHigh || !capability.Nature.Destructive {
			t.Fatalf("delete_dns_record 的声明是 %s / destructive=%v",
				capability.RiskLabel, capability.Nature.Destructive)
		}
		return
	}
	t.Fatal("声明里没有 delete_dns_record")
}

// ——— AC1：改动前先查当前值 ———

func TestExecute_UpdatingADNSRecord_QueriesTheCurrentValueFirst(t *testing.T) {
	fake := newFakeCloudflare(t, http.StatusOK,
		`{"success":true,"result":{"id":"rec1","type":"A","name":"www.example.com",`+
			`"content":"198.51.100.7","ttl":300,"proxied":false}}`)
	adapter := newAdapter(t, fake.URL)

	result, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
		Operation:   "update_dns_record",
		Resource:    resource(),
		Input:       json.RawMessage(`{"type":"A","name":"www.example.com","content":"198.51.100.7"}`),
		Credential:  credential(t),
		OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !result.OK {
		t.Fatalf("结果为失败：%+v", result.Error)
	}

	// 第一次必须是那条单条查询，第二次才是改动。
	first := <-fake.exchanges
	if first.method != http.MethodGet || first.path != "/zones/zone1/dns_records/rec1" {
		t.Fatalf("第一次请求是 %s %s，期望先查当前值", first.method, first.path)
	}
	second := <-fake.exchanges
	if second.method != http.MethodPut {
		t.Fatalf("第二次请求是 %s，期望 PUT", second.method)
	}

	// 旧值要能被审批页面读到（AC1）。
	byField := map[string]registry.ResourceChange{}
	for _, change := range result.Changes {
		byField[change.Field] = change
	}
	content, present := byField["content"]
	if !present {
		t.Fatalf("变化里没有 content：%+v", result.Changes)
	}
	if content.Before != "203.0.113.10" || content.After != "198.51.100.7" {
		t.Errorf("content 的对照为 %q → %q，期望 203.0.113.10 → 198.51.100.7",
			content.Before, content.After)
	}
	if content.Resource != "www.example.com" {
		t.Errorf("变化归属的资源为 %q", content.Resource)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	assertNoSentinel(t, string(encoded))
}

func TestExecute_DeletingADNSRecord_RecordsTheValueThatWillDisappear(t *testing.T) {
	fake := newFakeCloudflare(t, http.StatusOK, `{"success":true,"result":{"id":"rec1"}}`)
	adapter := newAdapter(t, fake.URL)

	result, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
		Operation: "delete_dns_record", Resource: resource(),
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	if len(result.Changes) == 0 {
		t.Fatal("删除没有记下即将消失的值")
	}
	for _, change := range result.Changes {
		if change.After != "" {
			t.Errorf("删除的 %s 有新值 %q，期望为空", change.Field, change.After)
		}
		if change.Before == "" {
			t.Errorf("删除的 %s 没有旧值", change.Field)
		}
	}
}

func TestExecute_WhenTheCurrentValueCannotBeRead_TheChangeIsNotMade(t *testing.T) {
	// 查不到旧值就不改：审批页面上会是一片空白，而用户是在对
	// 一个自己看不见的东西点同意。
	var seen atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, incoming *http.Request) {
			seen.Add(1)
			if incoming.Method != http.MethodGet {
				t.Errorf("查不到当前值时仍然发出了 %s", incoming.Method)
			}
			writer.WriteHeader(http.StatusNotFound)
		}))
	t.Cleanup(server.Close)
	adapter := newAdapter(t, server.URL)

	result, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
		Operation: "update_dns_record", Resource: resource(),
		Input:      json.RawMessage(`{"type":"A","name":"www","content":"1.2.3.4"}`),
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("执行返回了传输层错误：%v", err)
	}
	if result.OK {
		t.Fatal("查不到当前值却报告成功")
	}
	if seen.Load() != 1 {
		t.Errorf("共发出 %d 次请求，期望只有那次查询", seen.Load())
	}
}

func TestPreview_ReadsWithoutChangingAnything(t *testing.T) {
	fake := newFakeCloudflare(t, http.StatusOK, `{"success":true}`)
	adapter := newAdapter(t, fake.URL)

	preview, err := adapter.Preview(t.Context(), cloudflare.ExecuteRequest{
		Operation: "update_dns_record", Resource: resource(),
		Input:      json.RawMessage(`{"type":"A","name":"www.example.com","content":"198.51.100.7"}`),
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("查勘失败：%v", err)
	}
	if len(preview.Changes) == 0 {
		t.Fatal("查勘没有返回任何变化")
	}

	if got := <-fake.exchanges; got.method != http.MethodGet {
		t.Fatalf("查勘发出了 %s，它只该读", got.method)
	}
	if got := fake.requests.Load(); got != 1 {
		t.Errorf("查勘发出了 %d 次请求，期望 1 次", got)
	}
}

// ——— AC3：影响多于一条即大范围修改 ———

func TestPreview_AffectedRecordCount_DrivesTheBulkChangeFactor(t *testing.T) {
	// Adapter 只如实报告条数；「不是 1 条就是大范围修改」那条规则在 core/risk。
	cases := []struct {
		name      string
		operation string
		input     string
		expected  int
	}{
		{"改一条记录算一条", "update_dns_record", `{"type":"A","name":"w","content":"1.2.3.4"}`, 1},
		{"清一个文件算一条", "purge_cache", `{"files":["https://example.com/a"]}`, 1},
		{
			"清三个文件算三条", "purge_cache",
			`{"files":["https://example.com/a","https://example.com/b","https://example.com/c"]}`, 3,
		},
		{"清空全部无法确定", "purge_cache", `{"purge_everything":true}`, cloudflare.UnknownRecordCount},
		{"输入不是合法 JSON 时无法确定", "purge_cache", `{`, cloudflare.UnknownRecordCount},
		{"读取恒为一条", "read_zone", "", 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeCloudflare(t, http.StatusOK, `{"success":true}`)
			adapter := newAdapter(t, fake.URL)

			preview, err := adapter.Preview(t.Context(), cloudflare.ExecuteRequest{
				Operation: testCase.operation, Resource: resource(),
				Input:      json.RawMessage(testCase.input),
				Credential: credential(t), OperationID: operationID,
			})
			if err != nil {
				t.Fatalf("查勘失败：%v", err)
			}
			if preview.AffectedRecords != testCase.expected {
				t.Errorf("报告影响 %d 条，期望 %d 条",
					preview.AffectedRecords, testCase.expected)
			}
		})
	}
}

// ——— 七个能力域可调用且脱敏 ———

func TestExecute_EveryExecutableOperation_IsCallableAndRedacted(t *testing.T) {
	body := `{"success":true,"result":{"id":"x","name":"n","type":"A",` +
		`"content":"1.2.3.4","tunnel_secret":"` + sentinel.SentinelPrivateKey +
		`","api_token":"` + sentinel.SentinelToken + `","internal_note":"leak"}}`

	cases := []struct {
		operation string
		method    string
		path      string
		input     string
	}{
		{"read_zone", "GET", "/zones/zone1", ""},
		{"read_dns_records", "GET", "/zones/zone1/dns_records", ""},
		{"read_tunnel", "GET", "/accounts/acct1/cfd_tunnel/tun1", ""},
		{
			"create_dns_record", "POST", "/zones/zone1/dns_records",
			`{"type":"A","name":"w","content":"1.2.3.4"}`,
		},
		{"purge_cache", "POST", "/zones/zone1/purge_cache", `{"files":["https://x/a"]}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.operation+"可调用且结果已脱敏", func(t *testing.T) {
			fake := newFakeCloudflare(t, http.StatusOK, body)
			adapter := newAdapter(t, fake.URL)

			result, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
				Operation: testCase.operation, Resource: resource(),
				Input:      json.RawMessage(testCase.input),
				Credential: credential(t), OperationID: operationID,
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
			// 白名单里的字段必须真的回来了：全都被过滤掉的话，
			// 「没泄漏」是因为什么都没返回，那两条断言就成了空话。
			if !strings.Contains(string(result.Data), `"id":"x"`) {
				t.Errorf("返回体里没有白名单允许的 id 字段：%s", result.Data)
			}

			got := <-fake.exchanges
			if got.method != testCase.method || got.path != testCase.path {
				t.Errorf("假服务收到 %s %s，期望 %s %s",
					got.method, got.path, testCase.method, testCase.path)
			}
			if got.auth != "Bearer "+sentinel.SentinelToken {
				t.Errorf("凭据没有注入到 Authorization：%q", got.auth)
			}
		})
	}
}

// ——— 五项高风险只声明不执行 ———

func TestExecute_TheFiveHighRiskOperations_AreDeclaredButNotImplemented(t *testing.T) {
	operations := []string{
		"delete_zone", "manage_token", "manage_member",
		"bulk_update_dns", "update_security_rules",
	}

	for _, operation := range operations {
		t.Run(operation+"返回未实现且不发出请求", func(t *testing.T) {
			fake := newFakeCloudflare(t, http.StatusOK, `{"success":true}`)
			adapter := newAdapter(t, fake.URL)

			_, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
				Operation: operation, Resource: resource(),
				Credential: credential(t), OperationID: operationID,
			})
			if !apperr.Is(err, apperr.CodeNotImplemented) {
				t.Fatalf("错误码为 %s，期望 not_implemented（%v）", apperr.CodeOf(err), err)
			}
			if got := fake.requests.Load(); got != 0 {
				t.Errorf("未实现的操作仍然产生了 %d 次出站请求", got)
			}

			// Preview 同样不能成为绕过它的入口。
			if _, previewErr := adapter.Preview(t.Context(), cloudflare.ExecuteRequest{
				Operation: operation, Resource: resource(), OperationID: operationID,
			}); !apperr.Is(previewErr, apperr.CodeNotImplemented) {
				t.Errorf("查勘的错误码为 %s，期望 not_implemented", apperr.CodeOf(previewErr))
			}
		})
	}
}

// ——— 输入与失败路径 ———

func TestExecute_UndeclaredOperation_IsCapabilityNotOffered(t *testing.T) {
	fake := newFakeCloudflare(t, http.StatusOK, `{"success":true}`)
	adapter := newAdapter(t, fake.URL)

	_, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
		Operation: "create_zone", Resource: resource(), OperationID: operationID,
	})
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered", apperr.CodeOf(err))
	}
	if got := fake.requests.Load(); got != 0 {
		t.Errorf("未声明的操作产生了 %d 次出站请求", got)
	}
}

func TestExecute_ResourceValueThatWouldChangeTheEndpoint_IsRefused(t *testing.T) {
	cases := []struct {
		name   string
		zoneID string
	}{
		{"取值为空", ""},
		{"取值带斜杠", "zone1/../accounts"},
		{"取值带问号", "zone1?x=1"},
		{"取值想跳出去", ".."},
	}

	for _, testCase := range cases {
		t.Run(testCase.name+"时拒绝且不发出请求", func(t *testing.T) {
			fake := newFakeCloudflare(t, http.StatusOK, `{"success":true}`)
			adapter := newAdapter(t, fake.URL)

			target := resource()
			target["zone_id"] = testCase.zoneID

			_, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
				Operation: "read_zone", Resource: target, OperationID: operationID,
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

func TestExecute_UpstreamFailure_IsExplainedWithoutTheRawBody(t *testing.T) {
	cases := []struct {
		status int
		want   apperr.Code
	}{
		{http.StatusUnauthorized, apperr.CodeCredentialNotAuthorized},
		{http.StatusNotFound, apperr.CodeNotFound},
		{http.StatusConflict, apperr.CodeConflict},
		{http.StatusBadRequest, apperr.CodeInvalidRequest},
		{http.StatusBadGateway, apperr.CodeGatewayUnavailable},
	}

	for _, testCase := range cases {
		fake := newFakeCloudflare(t, testCase.status,
			`{"errors":[{"message":"`+sentinel.SentinelToken+`"}]}`)
		adapter := newAdapter(t, fake.URL)

		result, err := adapter.Execute(t.Context(), cloudflare.ExecuteRequest{
			Operation: "read_zone", Resource: resource(),
			Credential: credential(t), OperationID: operationID,
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

		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatalf("序列化失败：%v", marshalErr)
		}
		assertNoSentinel(t, string(encoded))
	}
}

func TestNew_DefaultsToTheCloudflareAPI(t *testing.T) {
	if cloudflare.DefaultBaseURL != "https://api.cloudflare.com/client/v4" {
		t.Errorf("默认地址为 %q", cloudflare.DefaultBaseURL)
	}
	adapter := newAdapter(t, "")
	if adapter.Service() != "cloudflare" || adapter.Kind() != registry.KindCloudflare {
		t.Errorf("服务名为 %q，种类为 %q", adapter.Service(), adapter.Kind())
	}
}
