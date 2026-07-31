package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

// StatusRunning 是进程能应答时唯一可能的状态。
//
// REQ-GATEWAY-002 里的 connected / connecting / disconnected 描述的是 Console 对
// Gateway 的观察结果：请求根本到不了时才是 disconnected，那由 Console 侧判定，
// 不是这个端点能返回的值。
const StatusRunning = "running"

// timeFormat 是 RFC3339 带毫秒，与数据库中的时间表示一致
const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// Status 是 GET /v1/gateway/status 的响应体（REQ-API-002）。
//
// 只含运行状态，不含任何业务数据、路径或凭据信息。
type Status struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	ListenAddress string `json:"listen_address"`
	WebAPIPort    int    `json:"web_api_port"`
	StartedAt     string `json:"started_at"`
}

type statusHandler struct {
	status Status
	logger *slog.Logger
}

func newStatusHandler(status Status, logger *slog.Logger) *statusHandler {
	return &statusHandler{status: status, logger: logger}
}

func (h *statusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, h.logger, http.StatusOK, h.status)
}

func formatTime(instant time.Time) string {
	return instant.UTC().Format(timeFormat)
}

// formatOptionalTime 把「还没发生」的时刻表示成空串而不是零时刻。
//
// 1970 年那个时间戳会被界面当成一个真实的过去时刻渲染出来，
// 而它要表达的其实是「这件事还没有发生」。
func formatOptionalTime(instant time.Time) string {
	if instant.IsZero() {
		return ""
	}
	return formatTime(instant)
}
