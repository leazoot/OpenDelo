package secret_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/secret"
)

// 一个不会自然出现在任何输出里的明文，便于断言「输出中不含它」。
const plaintextProbe = "PROBE_SECRET_9f3c1a_DO_NOT_LEAK"

func TestValue_Reveal_ReturnsPlaintext(t *testing.T) {
	// 这个用例保证其余用例不是因为 Value 根本没存住明文才通过的。
	v := secret.NewString(plaintextProbe)

	if got := string(v.Reveal()); got != plaintextProbe {
		t.Fatalf("Reveal() = %q，期望 %q", got, plaintextProbe)
	}
	if v.IsEmpty() {
		t.Error("IsEmpty() = true，期望 false")
	}
}

func TestValue_Format_EveryVerb_ReturnsRedacted(t *testing.T) {
	v := secret.NewString(plaintextProbe)

	verbs := []struct {
		name   string
		format string
	}{
		{name: "默认动词 %v", format: "%v"},
		{name: "字符串动词 %s", format: "%s"},
		{name: "带字段名的 %+v", format: "%+v"},
		{name: "Go 语法表示 %#v", format: "%#v"},
		{name: "带引号的 %q", format: "%q"},
		{name: "十进制 %d", format: "%d"},
		{name: "十六进制 %x", format: "%x"},
	}

	for _, verb := range verbs {
		t.Run(verb.name, func(t *testing.T) {
			if got := fmt.Sprintf(verb.format, v); got != secret.Redacted {
				t.Errorf("Sprintf(%q) = %q，期望 %q", verb.format, got, secret.Redacted)
			}
		})
	}
}

func TestValue_String_ReturnsRedacted(t *testing.T) {
	v := secret.NewString(plaintextProbe)

	if got := v.String(); got != secret.Redacted {
		t.Errorf("String() = %q，期望 %q", got, secret.Redacted)
	}

	// 字符串拼接不经 fmt 动词，由 Stringer 兜底。
	if got := "Authorization: Bearer " + v.String(); strings.Contains(got, plaintextProbe) {
		t.Errorf("拼接结果泄漏明文：%q", got)
	}
}

func TestValue_MarshalJSON_ReturnsRedacted(t *testing.T) {
	v := secret.NewString(plaintextProbe)

	t.Run("直接编码", func(t *testing.T) {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal 失败：%v", err)
		}
		if got, want := string(encoded), `"`+secret.Redacted+`"`; got != want {
			t.Errorf("json.Marshal = %s，期望 %s", got, want)
		}
	})

	t.Run("作为结构体的导出字段", func(t *testing.T) {
		encoded, err := json.Marshal(struct {
			Token secret.Value `json:"token"`
		}{Token: v})
		if err != nil {
			t.Fatalf("json.Marshal 失败：%v", err)
		}
		if got, want := string(encoded), `{"token":"`+secret.Redacted+`"}`; got != want {
			t.Errorf("json.Marshal = %s，期望 %s", got, want)
		}
	})
}

func TestValue_InUnexportedField_ReflectPrintDoesNotExposePlaintext(t *testing.T) {
	// 非导出字段里的 Value 无法被 fmt 调用方法，只能走反射打印。这是唯一
	// 绕过 Format 的路径，明文因此被放在指针后面（见 plaintextBox）。
	type carrier struct {
		credential secret.Value
	}
	held := carrier{credential: secret.NewString(plaintextProbe)}

	// 字节切片被反射打印时是 [80 82 79 ...]，逐字节同样是泄漏。
	digits := make([]string, 0, len(plaintextProbe))
	for _, b := range []byte(plaintextProbe) {
		digits = append(digits, strconv.Itoa(int(b)))
	}
	bytewise := "[" + strings.Join(digits, " ") + "]"

	for _, format := range []string{"%v", "%+v", "%#v"} {
		printed := fmt.Sprintf(format, held)

		if strings.Contains(printed, plaintextProbe) {
			t.Errorf("Sprintf(%q) 泄漏明文：%s", format, printed)
		}
		if strings.Contains(printed, bytewise) {
			t.Errorf("Sprintf(%q) 逐字节泄漏明文：%s", format, printed)
		}
	}
}

func TestValue_Zero_ClearsUnderlyingBytes(t *testing.T) {
	plaintext := []byte(plaintextProbe)
	v := secret.New(plaintext)

	v.Zero()

	for i, b := range plaintext {
		if b != 0 {
			t.Fatalf("Zero() 后第 %d 字节为 %d，期望 0；剩余内容 %q", i, b, plaintext)
		}
	}
	if v.Reveal() != nil {
		t.Errorf("Zero() 后 Reveal() = %v，期望 nil", v.Reveal())
	}
	if !v.IsEmpty() {
		t.Error("Zero() 后 IsEmpty() = false，期望 true")
	}
}

func TestValue_Zero_OnSharedCopy_ClearsAllCopies(t *testing.T) {
	// New 接管所有权，副本共享同一份明文；任一副本清零后其余副本不得仍可读。
	v := secret.NewString(plaintextProbe)
	copied := v

	v.Zero()

	if !copied.IsEmpty() {
		t.Errorf("副本在原值 Zero() 后仍可读：%q", copied.Reveal())
	}
}

func TestValue_ZeroValue_IsUsableAndEmpty(t *testing.T) {
	var v secret.Value

	if !v.IsEmpty() {
		t.Error("零值 IsEmpty() = false，期望 true")
	}
	if v.Reveal() != nil {
		t.Errorf("零值 Reveal() = %v，期望 nil", v.Reveal())
	}
	if got := fmt.Sprintf("%v", v); got != secret.Redacted {
		t.Errorf("零值 %%v = %q，期望 %q", got, secret.Redacted)
	}

	v.Zero() // 不得 panic
}

func TestValue_DoesNotImplementTextOrBinaryMarshaler(t *testing.T) {
	// encoding/json 与 slog 都优先使用这两个接口，实现任一个都会绕过
	// MarshalJSON 与 Format。编译期约束见 secret.go 末尾的 textMarshalerConflict。
	var v any = secret.Value{}

	if _, ok := v.(encoding.TextMarshaler); ok {
		t.Error("Value 实现了 encoding.TextMarshaler，它会被 json 与 slog 优先使用")
	}
	if _, ok := v.(encoding.BinaryMarshaler); ok {
		t.Error("Value 实现了 encoding.BinaryMarshaler")
	}
}

func TestValue_Slog_AtEveryLevel_LogsRedacted(t *testing.T) {
	handlers := map[string]func(*bytes.Buffer) slog.Handler{
		"JSON handler": func(buf *bytes.Buffer) slog.Handler {
			return slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		},
		"Text handler": func(buf *bytes.Buffer) slog.Handler {
			return slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		},
	}

	for name, newHandler := range handlers {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(newHandler(&buf))
			v := secret.NewString(plaintextProbe)

			// debug 级别同样脱敏，不存在「调试模式输出完整信息」。
			logger.Debug("取用凭据", "token", v)
			logger.Info("注入凭据", "token", v)

			logged := buf.String()
			if strings.Contains(logged, plaintextProbe) {
				t.Fatalf("日志泄漏明文：%s", logged)
			}
			if strings.Count(logged, secret.Redacted) != 2 {
				t.Errorf("期望两条日志各出现一次 %q，实际输出：%s", secret.Redacted, logged)
			}
		})
	}
}
