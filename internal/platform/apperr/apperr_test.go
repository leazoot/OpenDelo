package apperr_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// 两个不会自然出现在对外表示里的探针：一个走 cause，一个走 detail。
const (
	causeProbe  = "CAUSE_PROBE_5b2e7d /Users/someone/.opendelo/config.json"
	detailProbe = "DETAIL_PROBE_c41a09 host=api.github.com"
)

// requirementCodes 是需求文档中直接点名的错误码，逐项核对。
// 行号为该码在需求文档中首次出现的位置。
var requirementCodes = map[string]string{
	"agent_identity_unverifiable":        "REQ-AGENT-001（L62）",
	"session_expired":                    "REQ-AGENT-001（L63）",
	"capability_not_offered":             "REQ-CAP-001（L104）",
	"approval_timeout":                   "REQ-APPROVAL-004（L132）",
	"credential_not_authorized":          "REQ-CRED-002（L195）",
	"provider_unavailable":               "REQ-CRED-002（L197）",
	"provider_not_supported_on_platform": "REQ-CRED-003（L206）",
	"vault_locked":                       "REQ-CRED-004（L220）",
	"provider_locked_timeout":            "REQ-GATEWAY-004（L824）",
	"path_not_allowed":                   "REQ-ADAPTER-005（L681）",
	"adapter_timeout":                    "REQ-ADAPTER-008（L716）",
	"gateway_unavailable":                "REQ-GATEWAY-003（L813）",
}

func TestAll_CoversEveryCodeNamedInRequirements(t *testing.T) {
	registered := make(map[string]bool, len(apperr.All()))
	for _, code := range apperr.All() {
		registered[code.String()] = true
	}

	for name, source := range requirementCodes {
		if !registered[name] {
			t.Errorf("码表缺少 %q，它在 %s 中被点名要求", name, source)
		}
	}
}

func TestAll_EveryCode_IsValidAndHasMessage(t *testing.T) {
	all := apperr.All()
	if len(all) == 0 {
		t.Fatal("码表为空")
	}

	seen := make(map[string]bool, len(all))
	for _, code := range all {
		if !code.Valid() {
			t.Errorf("%q 未通过 Valid()", code)
		}
		if seen[code.String()] {
			t.Errorf("码名 %q 重复", code)
		}
		seen[code.String()] = true

		if apperr.New(code).Public().Message == "" {
			t.Errorf("%q 没有对外 message", code)
		}
	}
}

func TestAll_EveryMessage_IsEnglishAndSelfContained(t *testing.T) {
	// 用户决定 D-09：对外 message 走英文，因为这条通道的读者是 MCP 与 Proxy
	// 两个面的消费方（大模型与外部工具），Console 侧按 code 查自己的 i18n。
	// 靠 ASCII 扫描而不是靠约定：混进一个中文字符不会有任何检查发现，
	// 而两个面说法不一致的代价要到接入之后才看得见。
	for _, code := range apperr.All() {
		message := apperr.New(code).Public().Message
		for _, character := range message {
			if character > unicode.MaxASCII {
				t.Errorf("%q 的 message 含非 ASCII 字符 %q：%s", code, character, message)
				break
			}
		}
		if !strings.HasSuffix(message, ".") {
			t.Errorf("%q 的 message 不是一个完整句子：%s", code, message)
		}
	}
}

func TestError_Public_EveryCode_ExposesNoInternalDetail(t *testing.T) {
	// AC3 + AC4：每个码各一条用例，断言对外表示保留 operation_id、
	// 且不含 detail 与 cause 链的任何内容。
	for _, code := range apperr.All() {
		t.Run(code.String(), func(t *testing.T) {
			const operationID = "op_01J8Z9"

			failure := apperr.Wrap(code, errors.New(causeProbe)).
				WithDetail(detailProbe).
				WithOperationID(operationID)

			encoded, err := json.Marshal(failure.Public())
			if err != nil {
				t.Fatalf("序列化对外表示失败：%v", err)
			}
			serialized := string(encoded)

			for probe, where := range map[string]string{causeProbe: "cause", detailProbe: "detail"} {
				if strings.Contains(serialized, probe) {
					t.Errorf("对外表示泄漏了 %s：%s", where, serialized)
				}
			}
			if !strings.Contains(serialized, `"code":"`+code.String()+`"`) {
				t.Errorf("对外表示缺少错误码：%s", serialized)
			}
			if !strings.Contains(serialized, `"operation_id":"`+operationID+`"`) {
				t.Errorf("对外表示丢失 operation_id：%s", serialized)
			}
			if failure.Public().Message == "" {
				t.Error("对外 message 为空，用户看不到任何可读信息")
			}
		})
	}
}

func TestError_Error_KeepsDetailAndCauseForDiagnosis(t *testing.T) {
	// 这个用例保证上面的脱敏断言不是因为 detail 与 cause 根本没被保存才通过的。
	failure := apperr.Wrap(apperr.CodeVaultLocked, errors.New(causeProbe)).WithDetail(detailProbe)

	text := failure.Error()
	for _, probe := range []string{causeProbe, detailProbe, "vault_locked"} {
		if !strings.Contains(text, probe) {
			t.Errorf("Error() 丢失了 %q：%s", probe, text)
		}
	}
}

func TestError_ErrorsIs_MatchesByCodeThroughWrapping(t *testing.T) {
	root := apperr.New(apperr.CodeProviderUnavailable)
	wrapped := apperr.Wrap(apperr.CodeInternal, root)

	if !errors.Is(wrapped, apperr.New(apperr.CodeProviderUnavailable)) {
		t.Error("errors.Is 未能沿 cause 链匹配到 provider_unavailable")
	}
	if errors.Is(wrapped, apperr.New(apperr.CodeVaultLocked)) {
		t.Error("errors.Is 匹配到了链上不存在的码")
	}
	if !errors.Is(wrapped, root) {
		t.Error("errors.Is 未能匹配到链上的原始错误")
	}
}

func TestError_ErrorsAs_RecoversTypedError(t *testing.T) {
	root := apperr.New(apperr.CodeAdapterTimeout).WithOperationID("op_77")
	wrapped := errors.Join(errors.New("上游"), root)

	var recovered *apperr.Error
	if !errors.As(wrapped, &recovered) {
		t.Fatal("errors.As 未能取出 *apperr.Error")
	}
	if recovered.Code() != apperr.CodeAdapterTimeout {
		t.Errorf("Code() = %q，期望 adapter_timeout", recovered.Code())
	}
	if recovered.OperationID() != "op_77" {
		t.Errorf("OperationID() = %q，期望 op_77", recovered.OperationID())
	}
}

func TestError_Unwrap_ReturnsCause(t *testing.T) {
	cause := errors.New("底层失败")

	if unwrapped := errors.Unwrap(apperr.Wrap(apperr.CodeInternal, cause)); !errors.Is(unwrapped, cause) {
		t.Errorf("Unwrap() = %v，期望 %v", unwrapped, cause)
	}
	if unwrapped := errors.Unwrap(apperr.New(apperr.CodeInternal)); unwrapped != nil {
		t.Errorf("无 cause 时 Unwrap() = %v，期望 nil", unwrapped)
	}
}

func TestIs_MatchesCodeOnChain(t *testing.T) {
	wrapped := apperr.Wrap(apperr.CodeInternal, apperr.New(apperr.CodePathNotAllowed))

	if !apperr.Is(wrapped, apperr.CodePathNotAllowed) {
		t.Error("Is 未能匹配链上的 path_not_allowed")
	}
	if apperr.Is(nil, apperr.CodePathNotAllowed) {
		t.Error("Is(nil, ...) 返回了 true")
	}
	if apperr.Is(errors.New("外部错误"), apperr.CodeInternal) {
		t.Error("Is 把非 *apperr.Error 判成了 internal")
	}
}

func TestCodeOf_UnclassifiedError_IsInternal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperr.Code
	}{
		{name: "nil 返回零值码", err: nil, want: apperr.Code{}},
		{name: "外部错误折叠为 internal", err: errors.New(causeProbe), want: apperr.CodeInternal},
		{name: "链上的码原样返回", err: apperr.Wrap(apperr.CodeConflict, errors.New("x")), want: apperr.CodeConflict},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := apperr.CodeOf(testCase.err); got != testCase.want {
				t.Errorf("CodeOf() = %q，期望 %q", got, testCase.want)
			}
		})
	}
}

func TestPublicOf_NonAppError_FoldsToInternalWithoutLeaking(t *testing.T) {
	// 驱动与标准库的错误文本可能含连接串或路径，必须在出口处折叠掉。
	public := apperr.PublicOf(errors.New(causeProbe), "op_fallback")

	if public.Code != apperr.CodeInternal {
		t.Errorf("Code = %q，期望 internal", public.Code)
	}
	if strings.Contains(public.Message, causeProbe) {
		t.Errorf("对外 message 泄漏了原始错误：%s", public.Message)
	}
	if public.OperationID != "op_fallback" {
		t.Errorf("OperationID = %q，期望补上 op_fallback（REQ-API-003 AC3）", public.OperationID)
	}
}

func TestPublicOf_AppErrorWithoutOperationID_TakesFallback(t *testing.T) {
	public := apperr.PublicOf(apperr.New(apperr.CodeSessionExpired), "op_fallback")
	if public.Code != apperr.CodeSessionExpired {
		t.Errorf("Code = %q，期望 session_expired", public.Code)
	}
	if public.OperationID != "op_fallback" {
		t.Errorf("OperationID = %q，期望 op_fallback", public.OperationID)
	}

	carried := apperr.PublicOf(apperr.New(apperr.CodeSessionExpired).WithOperationID("op_own"), "op_fallback")
	if carried.OperationID != "op_own" {
		t.Errorf("OperationID = %q，错误自带的操作 ID 应当优先", carried.OperationID)
	}
}

func TestNew_ZeroCode_FallsBackToInternal(t *testing.T) {
	// 零值 Code 意味着调用方漏写了码。未知一律按最保守的情况处理。
	if got := apperr.New(apperr.Code{}).Code(); got != apperr.CodeInternal {
		t.Errorf("New(Code{}).Code() = %q，期望 internal", got)
	}
	if apperr.New(apperr.Code{}).Public().Message == "" {
		t.Error("零值码构造出的错误没有对外 message")
	}
}

func TestError_WithSetters_DoNotMutateOriginal(t *testing.T) {
	original := apperr.New(apperr.CodeForbidden)

	modified := original.WithOperationID("op_1").WithDetail(detailProbe)

	if modified.OperationID() != "op_1" {
		t.Errorf("副本未带上 operation_id：%q", modified.OperationID())
	}
	if !strings.Contains(modified.Error(), detailProbe) {
		t.Errorf("副本未带上 detail：%s", modified.Error())
	}
	if original.OperationID() != "" {
		t.Errorf("原错误被改动，OperationID = %q", original.OperationID())
	}
	if strings.Contains(original.Error(), detailProbe) {
		t.Errorf("原错误被改动：%s", original.Error())
	}
}

func TestCode_MarshalJSON_IsWireName(t *testing.T) {
	encoded, err := json.Marshal(apperr.CodeGatewayUnavailable)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if got, want := string(encoded), `"gateway_unavailable"`; got != want {
		t.Errorf("json.Marshal = %s，期望 %s", got, want)
	}
}
