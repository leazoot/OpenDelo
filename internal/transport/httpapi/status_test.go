package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

const statusPath = "/v1/gateway/status"

func decodeErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			OperationID string `json:"operation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误响应不是合法 JSON：%v，正文为 %q", err, response.Body.String())
	}
	if envelope.Error.Message == "" {
		t.Error("错误响应缺少 message")
	}
	if envelope.Error.OperationID == "" {
		t.Error("错误响应缺少 operation_id，用户无法在账本中定位这次请求")
	}
	return envelope.Error.Code
}

func TestGatewayStatus_ReportsRunningState(t *testing.T) {
	response := get(t, statusPath)

	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，期望 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Errorf("Content-Type 为 %q", contentType)
	}

	var status httpapi.Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}

	want := httpapi.Status{
		Status:        httpapi.StatusRunning,
		Version:       testVersion,
		ListenAddress: "127.0.0.1",
		WebAPIPort:    testPort,
		StartedAt:     startedAtText,
	}
	if status != want {
		t.Errorf("响应为 %+v，期望 %+v", status, want)
	}
}

func TestGatewayStatus_ExposesOnlyTheContractFields(t *testing.T) {
	// 这个端点在认证落地之前是开放的，多一个字段就是多一处泄露面。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(get(t, statusPath).Body.Bytes(), &fields); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)

	want := []string{"listen_address", "started_at", "status", "version", "web_api_port"}
	if !slices.Equal(names, want) {
		t.Errorf("字段集合为 %v，期望 %v", names, want)
	}
}

func TestGatewayStatus_CarriesOperationIDHeader(t *testing.T) {
	response := get(t, statusPath)

	operationID := response.Header().Get("X-Operation-ID")
	if len(operationID) != 26 {
		t.Errorf("X-Operation-ID 为 %q，期望 26 位 ULID", operationID)
	}
}

func TestGatewayStatus_WriteMethods_AreRejected(t *testing.T) {
	// 状态是只读的；状态变更一律走 POST/PATCH/DELETE，
	// 反过来也成立：这个端点不接受任何写方法。
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			response := do(t, method, statusPath)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("状态码为 %d，期望 405", response.Code)
			}
			if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("Allow 为 %q", allow)
			}
		})
	}
}

func TestUnknownVersionedPath_ReturnsNotFoundEnvelope(t *testing.T) {
	// /v1 下未注册的路径必须是 JSON 错误，而不是回落到 index.html ——
	// 前端拿到一段 HTML 只会解析失败，看不出到底发生了什么。
	response := get(t, "/v1/there-is-no-such-endpoint")

	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}
	if code := decodeErrorCode(t, response); code != "not_found" {
		t.Errorf("错误码为 %q，期望 not_found", code)
	}
}

func TestUnknownVersionedPath_ErrorMessageCarriesNoInternalDetail(t *testing.T) {
	// 对外 message 只能取自 apperr 的码表，路径这类请求内容不得出现在里面。
	response := get(t, "/v1/secret-internal-path")

	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("错误响应不是合法 JSON：%v", err)
	}
	if expected := apperr.New(apperr.CodeNotFound).Public().Message; envelope.Error.Message != expected {
		t.Errorf("message 为 %q，期望码表中的固定文本 %q", envelope.Error.Message, expected)
	}
	if strings.Contains(envelope.Error.Message, "secret-internal-path") {
		t.Errorf("message 里带上了请求路径：%q", envelope.Error.Message)
	}
}
