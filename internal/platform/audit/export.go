package audit

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"slices"
	"strconv"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

// Format 是导出格式（REQ-AUDIT-004）。设计稿 §07 的主按钮是「导出 JSONL」。
type Format string

const (
	// FormatJSON 输出一个 JSON 数组。
	FormatJSON Format = "json"
	// FormatJSONL 每行一个 JSON 对象，便于流式处理与追加。
	FormatJSONL Format = "jsonl"
	// FormatCSV 输出带表头的 CSV。
	FormatCSV Format = "csv"
)

var formats = []Format{FormatJSON, FormatJSONL, FormatCSV}

// Formats 返回全部导出格式的副本。
func Formats() []Format { return slices.Clone(formats) }

// exportColumns 是 CSV 的列顺序，也是三种格式共同的字段集合。
// 三者导出同一批字段，换格式不会换掉内容。
var exportColumns = []string{
	"id", "operation_id", "event_type",
	"agent_id", "device_id", "workspace_id",
	"identity_id", "credential_provider_id",
	"service", "operation", "resource", "resolved_scope",
	"verdict", "risk_level",
	"decision_id", "approval_id", "lease_id", "lease_status",
	"outcome", "response_status", "duration_ms", "is_redacted",
	"metadata", "created_at",
}

// ExportColumns 返回导出字段清单的副本，供测试与前端逐条核对。
func ExportColumns() []string { return slices.Clone(exportColumns) }

// Export 把事件写成指定格式。
//
// 每条记录在写出前**重新过一遍脱敏**（REQ-AUDIT-004 AC2）。写入时已经脱过一次，
// 这里是第二道：绕过 Recorder 直接写库的路径（迁移、修数据、将来的新代码）
// 不该让导出成为泄漏口。两道都在，才谈得上「导出与展示同一套规则」。
//
// 只往 writer 写，不发起任何网络请求 —— 导出是本地文件下载（AC3）。
func Export(writer io.Writer, format Format, events []Event) error {
	if writer == nil {
		return apperr.New(apperr.CodeInvalidRequest).WithDetail("导出需要一个写入目标")
	}
	if !slices.Contains(formats, format) {
		return apperr.New(apperr.CodeInvalidRequest).WithDetail("不支持的导出格式 " + string(format))
	}

	// 用 make 而不是 var：空结果必须导出成 []，nil 切片会被序列化成 null，
	// 消费方要为此多一条分支。
	rows := make([]map[string]any, 0, len(events))
	for _, event := range events {
		row, err := exportRow(event)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}

	switch format {
	case FormatJSON:
		return writeJSON(writer, rows)
	case FormatJSONL:
		return writeJSONL(writer, rows)
	case FormatCSV:
		return writeCSV(writer, rows)
	default:
		return apperr.New(apperr.CodeInternal).WithDetail("导出格式分支缺失 " + string(format))
	}
}

func exportRow(event Event) (map[string]any, error) {
	resource, err := redactJSONObject("resource", event.Resource)
	if err != nil {
		return nil, err
	}
	resolvedScope, err := redactJSONObject("resolved_scope", event.ResolvedScope)
	if err != nil {
		return nil, err
	}
	metadata, err := redactMetadata(event.Metadata)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"id":                     event.ID,
		"operation_id":           event.OperationID,
		"event_type":             string(event.Type),
		"agent_id":               event.AgentID,
		"device_id":              event.DeviceID,
		"workspace_id":           event.WorkspaceID,
		"identity_id":            event.IdentityID,
		"credential_provider_id": event.CredentialProviderID,
		"service":                event.Service,
		"operation":              event.Operation,
		"resource":               resource,
		"resolved_scope":         resolvedScope,
		"verdict":                string(event.Verdict),
		"risk_level":             string(event.RiskLevel),
		"decision_id":            event.DecisionID,
		"approval_id":            event.ApprovalID,
		"lease_id":               event.LeaseID,
		"lease_status":           string(event.LeaseStatus),
		"outcome":                string(event.Outcome),
		"response_status":        event.ResponseStatus,
		"duration_ms":            int(event.Duration.Milliseconds()),
		"is_redacted":            event.IsRedacted,
		"metadata":               metadata,
		"created_at":             event.CreatedAt.UTC().Format(timeFormat),
	}, nil
}

// timeFormat 与账本存储的时间格式一致：UTC 的 RFC3339 带毫秒。
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

func writeJSON(writer io.Writer, rows []map[string]any) error {
	encoder := json.NewEncoder(writer)
	if err := encoder.Encode(rows); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("写出 JSON 导出失败")
	}
	return nil
}

func writeJSONL(writer io.Writer, rows []map[string]any) error {
	encoder := json.NewEncoder(writer)
	for _, row := range rows {
		if err := encoder.Encode(row); err != nil {
			return apperr.Wrap(apperr.CodeInternal, err).WithDetail("写出 JSONL 导出失败")
		}
	}
	return nil
}

func writeCSV(writer io.Writer, rows []map[string]any) error {
	encoder := csv.NewWriter(writer)
	if err := encoder.Write(exportColumns); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("写出 CSV 表头失败")
	}

	for _, row := range rows {
		record := make([]string, 0, len(exportColumns))
		for _, column := range exportColumns {
			record = append(record, csvValue(row[column]))
		}
		if err := encoder.Write(record); err != nil {
			return apperr.Wrap(apperr.CodeInternal, err).WithDetail("写出 CSV 行失败")
		}
	}

	encoder.Flush()
	if err := encoder.Error(); err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("刷新 CSV 导出失败")
	}
	return nil
}

func csvValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}
