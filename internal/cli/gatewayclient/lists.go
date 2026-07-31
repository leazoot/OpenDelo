package gatewayclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

/*
 * CLI 的三条只读命令要用的列表（REQ-CLI-001 的 P1 部分）。
 *
 * 列表一律分页，与 API 的分页约定一致：
 * 一条无界的查询在十万条账本上会把终端刷爆，也会击穿 REQ-NFR-001 的预算。
 */

// DefaultLimit 与 API 的默认页大小一致。
const DefaultLimit = 50

// List 是 API 列表端点的响应形状（`{"items":[...],"next_cursor":"..."}`）。
type List[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// Identities 列出已连接的身份。
func Identities(ctx context.Context, address, sessionToken string, limit int) (List[httpapi.IdentityView], error) {
	var result List[httpapi.IdentityView]
	err := call(ctx, address, sessionToken, http.MethodGet,
		"/v1/identities"+limitQuery(limit), nil, &result)
	return result, err
}

// Leases 列出生效中的 Lease。
func Leases(ctx context.Context, address, sessionToken string, limit int) (List[httpapi.LeaseView], error) {
	var result List[httpapi.LeaseView]
	err := call(ctx, address, sessionToken, http.MethodGet,
		"/v1/leases"+limitQuery(limit), nil, &result)
	return result, err
}

// AuditFilter 是账本查询的过滤条件。
//
// AgentID 与 Service 二选一：其余维度上没有索引，组合过滤会退化成全表扫描。
// API 侧对同时给出两者直接返回 400，这里如实转达而不是替用户挑一个。
type AuditFilter struct {
	AgentID string
	Service string
	Limit   int
}

// AuditEvents 列出账本记录，按时间倒序。
func AuditEvents(
	ctx context.Context, address, sessionToken string, filter AuditFilter,
) (List[httpapi.AuditEventView], error) {
	if filter.AgentID != "" && filter.Service != "" {
		return List[httpapi.AuditEventView]{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("--agent 与 --service 只能二选一")
	}

	query := url.Values{}
	query.Set("limit", strconv.Itoa(pageSize(filter.Limit)))
	if filter.AgentID != "" {
		query.Set("agent_id", filter.AgentID)
	}
	if filter.Service != "" {
		query.Set("service", filter.Service)
	}

	var result List[httpapi.AuditEventView]
	err := call(ctx, address, sessionToken, http.MethodGet,
		"/v1/audit-events?"+query.Encode(), nil, &result)
	return result, err
}

func limitQuery(limit int) string {
	return "?limit=" + strconv.Itoa(pageSize(limit))
}

func pageSize(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return limit
}
