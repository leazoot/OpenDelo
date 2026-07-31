package audit_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
)

func exportableEvents(count int) []audit.Event {
	events := make([]audit.Event, 0, count)
	for index := range count {
		events = append(events, audit.Event{
			ID:             "01K1EVENT0000000000000000" + string(rune('A'+index)),
			OperationID:    "01K1OPERATION00000000000000",
			Type:           audit.EventAdapterExecuted,
			Service:        "github",
			Operation:      "repo.read",
			Resource:       `{"repo":"opendelo"}`,
			ResolvedScope:  `{"service":"github"}`,
			Outcome:        audit.OutcomeSucceeded,
			ResponseStatus: 200,
			Duration:       120 * time.Millisecond,
			IsRedacted:     true,
			Metadata:       `{"match_level":"trust_memory"}`,
			CreatedAt:      recordedAt,
		})
	}
	return events
}

func TestExport_AllThreeFormats_CarryTheSameRecordCount(t *testing.T) {
	// REQ-AUDIT-004 AC1：三种格式的条数与查询结果一致。
	const count = 5
	events := exportableEvents(count)

	for _, format := range audit.Formats() {
		t.Run(string(format), func(t *testing.T) {
			var buffer bytes.Buffer
			if err := audit.Export(&buffer, format, events); err != nil {
				t.Fatalf("导出失败：%v", err)
			}
			if got := countExported(t, format, buffer.Bytes()); got != count {
				t.Errorf("导出了 %d 条，期望 %d 条", got, count)
			}
		})
	}
}

func TestExport_EmptyResult_IsStillWellFormed(t *testing.T) {
	// 空结果不该导出成 null 或者一个空文件：消费方会因此多一条分支。
	for _, format := range audit.Formats() {
		t.Run(string(format), func(t *testing.T) {
			var buffer bytes.Buffer
			if err := audit.Export(&buffer, format, nil); err != nil {
				t.Fatalf("导出失败：%v", err)
			}
			if got := countExported(t, format, buffer.Bytes()); got != 0 {
				t.Errorf("空结果导出了 %d 条", got)
			}
			if format == audit.FormatJSON && !strings.HasPrefix(strings.TrimSpace(buffer.String()), "[") {
				t.Errorf("空结果的 JSON 不是数组：%s", buffer.String())
			}
			if format == audit.FormatCSV && !strings.HasPrefix(buffer.String(), "id,") {
				t.Errorf("空结果的 CSV 没有表头：%s", buffer.String())
			}
		})
	}
}

func TestExport_RedactsEvenWhenTheStoredRowWasNot(t *testing.T) {
	// REQ-AUDIT-004 AC2：导出经过与展示相同的脱敏规则。
	//
	// 这里刻意跳过 Recorder，直接拿一条没脱过敏的事件去导出 ——
	// 绕过写入器直接写库的路径（迁移、修数据、将来的新代码）不该让导出成为泄漏口。
	const leak = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"
	raw := exportableEvents(1)
	raw[0].Resource = `{"headers":{"authorization":"` + leak + `"}}`
	raw[0].Metadata = `{"api_key":"` + leak + `"}`

	for _, format := range audit.Formats() {
		t.Run(string(format), func(t *testing.T) {
			var buffer bytes.Buffer
			if err := audit.Export(&buffer, format, raw); err != nil {
				t.Fatalf("导出失败：%v", err)
			}
			if strings.Contains(buffer.String(), leak) {
				t.Errorf("导出内容里出现了哨兵：%s", buffer.String())
			}
			if !strings.Contains(buffer.String(), audit.Redacted) {
				t.Errorf("导出内容里没有出现 %s：%s", audit.Redacted, buffer.String())
			}
		})
	}
}

func TestExport_EveryFormatCarriesTheSameFields(t *testing.T) {
	// 换格式不该换掉内容：三者导出同一批字段。
	events := exportableEvents(1)

	var jsonBuffer, jsonlBuffer, csvBuffer bytes.Buffer
	for _, pair := range []struct {
		format audit.Format
		buffer *bytes.Buffer
	}{
		{audit.FormatJSON, &jsonBuffer},
		{audit.FormatJSONL, &jsonlBuffer},
		{audit.FormatCSV, &csvBuffer},
	} {
		if err := audit.Export(pair.buffer, pair.format, events); err != nil {
			t.Fatalf("导出 %s 失败：%v", pair.format, err)
		}
	}

	var asArray []map[string]any
	if err := json.Unmarshal(jsonBuffer.Bytes(), &asArray); err != nil {
		t.Fatalf("解析 JSON 导出失败：%v", err)
	}
	var asLine map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(jsonlBuffer.Bytes()), &asLine); err != nil {
		t.Fatalf("解析 JSONL 导出失败：%v", err)
	}

	columns := audit.ExportColumns()
	for _, column := range columns {
		if _, present := asArray[0][column]; !present {
			t.Errorf("JSON 导出缺少字段 %q", column)
		}
		if _, present := asLine[column]; !present {
			t.Errorf("JSONL 导出缺少字段 %q", column)
		}
	}
	if len(asArray[0]) != len(columns) {
		t.Errorf("JSON 导出有 %d 个字段，期望 %d 个", len(asArray[0]), len(columns))
	}

	header, _, found := strings.Cut(csvBuffer.String(), "\n")
	if !found {
		t.Fatal("CSV 导出没有表头")
	}
	if header != strings.Join(columns, ",") {
		t.Errorf("CSV 表头是 %q，期望 %q", header, strings.Join(columns, ","))
	}
}

func TestExport_UnsupportedFormatAndMissingWriter_AreRejected(t *testing.T) {
	var buffer bytes.Buffer

	assertCode(t, audit.Export(&buffer, "xml", exportableEvents(1)), apperr.CodeInvalidRequest)
	assertCode(t, audit.Export(&buffer, "", exportableEvents(1)), apperr.CodeInvalidRequest)
	assertCode(t, audit.Export(nil, audit.FormatJSON, exportableEvents(1)), apperr.CodeInvalidRequest)
}

func TestExport_MalformedStoredRow_IsReportedNotSilentlySkipped(t *testing.T) {
	// 一条读不出来的记录不能被悄悄跳过：那会让导出的条数与查询结果对不上，
	// 而 AC1 要求两者一致。
	raw := exportableEvents(1)
	raw[0].Metadata = "not json"

	var buffer bytes.Buffer
	assertCode(t, audit.Export(&buffer, audit.FormatJSONL, raw), apperr.CodeInvalidRequest)
}

func TestExport_WriterFailure_IsPropagated(t *testing.T) {
	// 导出写到一半失败必须让调用方知道，否则用户会拿到一个残缺的文件却以为是完整的。
	failing := &failingWriter{err: errors.New("磁盘满了")}

	for _, format := range audit.Formats() {
		t.Run(string(format), func(t *testing.T) {
			if err := audit.Export(failing, format, exportableEvents(2)); err == nil {
				t.Error("写入目标失败时导出却成功了")
			}
		})
	}
}

type failingWriter struct{ err error }

func (f *failingWriter) Write([]byte) (int, error) { return 0, f.err }

// countExported 按格式数出导出的记录条数。
func countExported(t *testing.T, format audit.Format, content []byte) int {
	t.Helper()

	switch format {
	case audit.FormatJSON:
		var rows []map[string]any
		if err := json.Unmarshal(content, &rows); err != nil {
			t.Fatalf("解析 JSON 导出失败：%v", err)
		}
		return len(rows)
	case audit.FormatJSONL:
		trimmed := strings.TrimSpace(string(content))
		if trimmed == "" {
			return 0
		}
		lines := strings.Split(trimmed, "\n")
		for index, line := range lines {
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("第 %d 行不是合法 JSON：%v", index, err)
			}
		}
		return len(lines)
	case audit.FormatCSV:
		records, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
		if err != nil {
			t.Fatalf("解析 CSV 导出失败：%v", err)
		}
		return len(records) - 1 // 去掉表头
	default:
		t.Fatalf("未知格式 %q", format)
		return 0
	}
}
