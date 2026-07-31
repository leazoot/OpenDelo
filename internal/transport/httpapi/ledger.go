package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * History API：账本查询与导出（PRD §27、REQ-AUDIT-003/004）。
 *
 * 账本只在本地追加、只读出去，**不上传**（设计稿 §07）：这两个端点写文件到
 * 响应体，没有任何出站调用 —— `test/arch` 的出站扫描会挡下往这里加一个
 * http.Client 的改动。
 *
 * 导出与展示走的是同一个 view 函数，因此 REQ-AUDIT-004 AC2
 * 「导出内容经过与展示相同的脱敏」不是靠两处各自记得做同一件事。
 */

// exportFormats 是三种导出格式（REQ-AUDIT-004）。
const (
	formatJSON  = "json"
	formatJSONL = "jsonl"
	formatCSV   = "csv"
)

// ledgerFilter 是账本查询的过滤条件。
//
// 只有三种：不过滤、按 Agent、按服务。这三条各自对应一个既有索引
// （`idx_audit_events_created_at` 与另外两个复合索引），因此每种组合都走索引，
// 不存在一次全表扫描（REQ-NFR-001 的 Ledger 查询预算）。
type ledgerFilter struct {
	AgentID string
	Service string
	Before  time.Time
	Limit   int
}

func ledgerFilterFrom(r *http.Request) (ledgerFilter, error) {
	limit, err := limitFrom(r)
	if err != nil {
		return ledgerFilter{}, err
	}

	query := r.URL.Query()
	filter := ledgerFilter{
		AgentID: query.Get("agent_id"),
		Service: query.Get("service"),
		Limit:   limit,
	}
	if filter.AgentID != "" && filter.Service != "" {
		// 两个条件一起用会退化成全表扫描后再过滤。与其让它慢下来，
		// 不如说清楚这一版支持哪些组合。
		return ledgerFilter{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("agent_id 与 service 只能二选一")
	}

	if raw := query.Get("before"); raw != "" {
		before, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return ledgerFilter{}, apperr.Wrap(apperr.CodeInvalidRequest, parseErr).
				WithDetail("before 必须是 RFC3339 时刻")
		}
		filter.Before = before.UTC()
	}
	return filter, nil
}

// ledgerEvents 按过滤条件取出记录。
func (e *endpoints) ledgerEvents(r *http.Request, filter ledgerFilter) ([]audit.Event, error) {
	switch {
	case filter.AgentID != "":
		return e.services.Ledger.EventsByAgent(
			r.Context(), filter.AgentID, filter.Before, filter.Limit)
	case filter.Service != "":
		return e.services.Ledger.EventsByService(
			r.Context(), filter.Service, filter.Before, filter.Limit)
	default:
		return e.services.Ledger.Events(r.Context(), filter.Before, filter.Limit)
	}
}

// listEvents 返回账本条目（Boundary Ledger 页面）。
func (e *endpoints) listEvents(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "账本"); err != nil {
		return
	}

	filter, err := ledgerFilterFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	found, err := e.ledgerEvents(r, filter)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	items := make([]AuditEventView, 0, len(found))
	for _, event := range found {
		items = append(items, auditEventView(event))
	}
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[AuditEventView]{
		Items:      items,
		NextCursor: nextCursor(found),
	})
}

// nextCursor 是下一页的游标：本页最后一条的时刻。
// 本页为空表示没有更早的记录了。
func nextCursor(events []audit.Event) string {
	if len(events) == 0 {
		return ""
	}
	return formatTime(events[len(events)-1].CreatedAt)
}

// showEvent 返回一条账本记录。
func (e *endpoints) showEvent(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "账本"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	event, err := e.services.Ledger.EventByID(r.Context(), id)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusOK, auditEventView(event))
}

// exportEvents 把当前过滤条件下的记录导出为本地文件（REQ-AUDIT-004）。
//
// 三种格式导出的是**同一批**记录：过滤与取数只有一处，格式只影响写法。
func (e *endpoints) exportEvents(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "账本"); err != nil {
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		// 设计稿 §07 的主按钮是「导出 JSONL」。
		format = formatJSONL
	}
	if format != formatJSON && format != formatJSONL && format != formatCSV {
		writeValidationError(w, r, e.logger, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("format 只能是 json、jsonl 或 csv"), "format")
		return
	}

	filter, err := ledgerFilterFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	found, err := e.ledgerEvents(r, filter)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	views := make([]AuditEventView, 0, len(found))
	for _, event := range found {
		views = append(views, auditEventView(event))
	}

	w.Header().Set(headerContentType, exportContentTypes[format])
	w.Header().Set("Content-Disposition",
		`attachment; filename="opendelo-ledger.`+format+`"`)
	writeExport(w, r, e.logger, format, views)
}

var exportContentTypes = map[string]string{
	formatJSON:  contentTypeJSON,
	formatJSONL: "application/x-ndjson; charset=utf-8",
	formatCSV:   "text/csv; charset=utf-8",
}

func writeExport(
	w http.ResponseWriter, r *http.Request, logger *slog.Logger,
	format string, views []AuditEventView,
) {
	var err error
	switch format {
	case formatJSON:
		err = json.NewEncoder(w).Encode(views)
	case formatJSONL:
		err = writeJSONLines(w, views)
	default:
		err = writeCSV(w, views)
	}
	if err != nil {
		// 响应头已经发出，状态码改不了，只能记录。正文不进日志。
		logger.ErrorContext(r.Context(), "写出导出内容失败",
			slog.String("format", format),
			slog.String("operation_id", logging.OperationIDFrom(r.Context())))
	}
}

func writeJSONLines(w http.ResponseWriter, views []AuditEventView) error {
	encoder := json.NewEncoder(w)
	for _, view := range views {
		if err := encoder.Encode(view); err != nil {
			return err
		}
	}
	return nil
}

// csvColumns 是 CSV 的表头，顺序即列序。
//
// 与 AuditEventView 的字段一一对应：少一列就是导出比展示少了信息，
// 用例逐列核对（REQ-AUDIT-004 AC1）。
var csvColumns = []string{
	"id", "operation_id", "type", "agent_id", "device_id", "workspace_id",
	"identity_id", "service", "operation", "resource", "resolved_scope",
	"verdict", "risk_level", "decision_id", "approval_id", "lease_id",
	"lease_status", "outcome", "response_status", "duration_ms",
	"is_redacted", "metadata", "created_at",
}

func writeCSV(w http.ResponseWriter, views []AuditEventView) error {
	writer := csv.NewWriter(w)
	if err := writer.Write(csvColumns); err != nil {
		return err
	}
	for _, view := range views {
		if err := writer.Write(view.row()); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}
