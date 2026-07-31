package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

const valueProbe = "VALUE_PROBE_a7d21e_DO_NOT_LEAK"

// securityRuleWords 是安全规则列出的十个词，逐条核对（AC1）。
var securityRuleWords = []string{
	"authorization", "cookie", "set-cookie", "token", "api_key",
	"apikey", "password", "secret", "private_key", "credential",
}

func newTestLogger(t *testing.T, level slog.Level) (*slog.Logger, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	return logging.New(logging.Options{Level: level, Writer: &buf}), &buf
}

func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("日志不是合法 JSON：%v；原文：%s", err, line)
	}
	return record
}

func TestSensitiveKeyWords_CoversSecurityRuleList(t *testing.T) {
	present := make(map[string]bool)
	for _, word := range logging.SensitiveKeyWords() {
		present[word] = true
	}

	for _, word := range securityRuleWords {
		if !present[word] {
			t.Errorf("脱敏词表缺少 %q", word)
		}
	}
	if len(logging.SensitiveKeyWords()) != len(securityRuleWords) {
		t.Errorf("词表有 %d 个词，规则文件列出 %d 个", len(logging.SensitiveKeyWords()), len(securityRuleWords))
	}
}

func TestNew_SensitiveKey_ValueIsRedacted(t *testing.T) {
	// 词表原词 + 真实会遇到的变体：大小写、连字符、驼峰、前后缀。
	keys := append([]string(nil), securityRuleWords...)
	keys = append(keys,
		"Authorization", "Set-Cookie", "X-API-Key", "apiKey",
		"access_token", "refresh-token", "db_password", "PRIVATE_KEY",
		"client_secret", "credential_reference",
	)

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			logger, buf := newTestLogger(t, slog.LevelInfo)
			logger.Info("注入凭据", key, valueProbe)

			logged := buf.String()
			if strings.Contains(logged, valueProbe) {
				t.Fatalf("字段 %q 未脱敏：%s", key, logged)
			}
			if record := decodeLine(t, logged); record[key] != logging.Redacted {
				t.Errorf("字段 %q = %v，期望 %q", key, record[key], logging.Redacted)
			}
		})
	}
}

func TestNew_NonSensitiveKey_ValueIsPreserved(t *testing.T) {
	// 若一切都被脱敏，上面的用例就没有意义了。日志必须仍然可用。
	logger, buf := newTestLogger(t, slog.LevelInfo)
	logger.Info("决策完成", "agent_id", "agt_01", "service", "github", "risk_level", "high")

	record := decodeLine(t, buf.String())
	for key, want := range map[string]string{"agent_id": "agt_01", "service": "github", "risk_level": "high"} {
		if record[key] != want {
			t.Errorf("字段 %q = %v，期望 %q", key, record[key], want)
		}
	}
}

func TestNew_DebugLevel_StillRedacts(t *testing.T) {
	// 不存在「调试模式输出完整信息」的开关（AC3）。
	logger, buf := newTestLogger(t, slog.LevelDebug)
	logger.Debug("排查用", "token", valueProbe)

	logged := buf.String()
	if logged == "" {
		t.Fatal("debug 级别没有输出，用例无效")
	}
	if strings.Contains(logged, valueProbe) {
		t.Fatalf("debug 级别泄漏：%s", logged)
	}
}

func TestNew_WithAttrs_RedactsPreformattedFields(t *testing.T) {
	// logger.With 预先格式化的字段走的是另一条码路，同样必须脱敏。
	logger, buf := newTestLogger(t, slog.LevelInfo)
	logger.With("authorization", valueProbe).Info("出站请求")

	if strings.Contains(buf.String(), valueProbe) {
		t.Fatalf("With 预置的字段未脱敏：%s", buf.String())
	}
}

func TestNew_SecretValueUnderInnocuousKey_IsRedacted(t *testing.T) {
	// 词表只看 key，看不出「无害 key 下藏着凭据」。这一面由 secret.Value 兜底。
	logger, buf := newTestLogger(t, slog.LevelInfo)
	logger.Info("注入凭据", "value", secret.NewString(valueProbe))

	logged := buf.String()
	if strings.Contains(logged, valueProbe) {
		t.Fatalf("secret.Value 未脱敏：%s", logged)
	}
	if record := decodeLine(t, logged); record["value"] != logging.Redacted {
		t.Errorf("字段 value = %v，期望 %q", record["value"], logging.Redacted)
	}
}

func TestRedacted_MatchesSecretRedacted(t *testing.T) {
	// 两个常量必须一致，否则日志里会出现两种脱敏标记。
	if logging.Redacted != secret.Redacted {
		t.Errorf("logging.Redacted = %q，secret.Redacted = %q，二者必须一致", logging.Redacted, secret.Redacted)
	}
}

func TestNew_WithOperationIDInContext_EveryRecordCarriesIt(t *testing.T) {
	logger, buf := newTestLogger(t, slog.LevelDebug)
	ctx := logging.WithOperationID(context.Background(), "op_01J8ZKQ")

	logger.DebugContext(ctx, "第一条")
	logger.InfoContext(ctx, "第二条")
	logger.WarnContext(ctx, "第三条")
	logger.ErrorContext(ctx, "第四条")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("输出了 %d 条日志，期望 4：%s", len(lines), buf.String())
	}
	for i, line := range lines {
		if got := decodeLine(t, line)[logging.OperationIDKey]; got != "op_01J8ZKQ" {
			t.Errorf("第 %d 条的 %s = %v，期望 op_01J8ZKQ", i+1, logging.OperationIDKey, got)
		}
	}
}

func TestNew_WithoutOperationID_FieldIsEmptyStringNotMissing(t *testing.T) {
	logger, buf := newTestLogger(t, slog.LevelInfo)

	logger.Info("没有 context 的调用")

	record := decodeLine(t, buf.String())
	operationID, present := record[logging.OperationIDKey]
	if !present {
		t.Fatalf("缺少 %s 字段：%s", logging.OperationIDKey, buf.String())
	}
	if operationID != "" {
		t.Errorf("%s = %v，期望空字符串", logging.OperationIDKey, operationID)
	}
}

func TestOperationIDFrom_MissingOrNilContext_ReturnsEmptyString(t *testing.T) {
	type unrelatedKey struct{}

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{name: "nil context 不 panic", ctx: nil},
		{name: "未携带操作 ID", ctx: context.Background()},
		{name: "携带的不是字符串", ctx: context.WithValue(context.Background(), unrelatedKey{}, 42)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := logging.OperationIDFrom(testCase.ctx); got != "" {
				t.Errorf("OperationIDFrom() = %q，期望空字符串", got)
			}
		})
	}
}

func TestWithOperationID_NilContext_DoesNotPanic(t *testing.T) {
	//nolint:staticcheck // 显式验证 nil context 的兜底行为
	ctx := logging.WithOperationID(nil, "op_1")

	if got := logging.OperationIDFrom(ctx); got != "op_1" {
		t.Errorf("OperationIDFrom() = %q，期望 op_1", got)
	}
}

func TestOptions_ExposesNoSwitchThatCouldDisableRedaction(t *testing.T) {
	// AC5：脱敏不可配置。新增任何字段都会让这条用例失败，迫使人重新审视它是否
	// 提供了关闭脱敏的途径。
	allowed := map[string]bool{"Level": true, "Writer": true}

	optionsType := reflect.TypeOf(logging.Options{})
	for i := range optionsType.NumField() {
		name := optionsType.Field(i).Name
		if !allowed[name] {
			t.Errorf("Options 新增了字段 %q；若它能影响脱敏则违反 AC5，确认无害后再加入白名单", name)
		}
	}
	if optionsType.NumField() != len(allowed) {
		t.Errorf("Options 有 %d 个字段，期望 %d 个", optionsType.NumField(), len(allowed))
	}
}
