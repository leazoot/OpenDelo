package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * Lease 端点（REQ-LEASE-002/003）。
 *
 * 三个端点合起来只能让授权变少。「延长」这件事在这里测不出来，
 * 因为根本没有对应的端点与方法 —— 见 TestRoutes_MatchTheEndpointListInThePRD。
 */

// issued 走审批放行产出一条真实的 Lease。
func issued(t *testing.T, all api) httpapi.LeaseView {
	t.Helper()

	item := waiting(t, all)
	response := all.call(t, http.MethodPost, "/v1/approvals/"+item.ID+"/allow-task", "")
	if response.Code != http.StatusOK {
		t.Fatalf("放行失败：%d %s", response.Code, response.Body.String())
	}

	var settled settlement
	decodeInto(t, response, &settled)
	if settled.Lease == nil {
		t.Fatal("放行之后没有 Lease")
	}
	return *settled.Lease
}

func TestListLeases_ReturnsActiveOnes(t *testing.T) {
	all := newAPI(t)
	granted := issued(t, all)

	response := all.call(t, http.MethodGet, "/v1/leases", "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var envelope struct {
		Items []httpapi.LeaseView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 1 || envelope.Items[0].ID != granted.ID {
		t.Fatalf("返回了 %d 条 Lease：%v", len(envelope.Items), envelope.Items)
	}
	if envelope.Items[0].ExpiresAt == "" {
		t.Error("Lease 没有到期时刻，「永久授权」在这个产品里不可表达")
	}
}

func TestListLeases_AgentSeesOnlyItsOwn(t *testing.T) {
	// Lease 由 Console 放行产出，再换一个别的 Agent 来看这份数据。
	all := newAPI(t)
	issued(t, all)

	other := newAPIForBackend(t, all.backend, httpapi.Caller{AgentID: "agent-b"})
	response := other.call(t, http.MethodGet, "/v1/leases", "")
	var envelope struct {
		Items []httpapi.LeaseView `json:"items"`
	}
	decodeInto(t, response, &envelope)
	if len(envelope.Items) != 0 {
		t.Errorf("别的 Agent 的 Lease 出现在列表里：%v", envelope.Items)
	}
}

func TestListLeases_LimitOutOfRangeIsRefused(t *testing.T) {
	// 认不出的 limit 直接拒绝，而不是悄悄换成默认值。
	all := newAPI(t)

	for _, raw := range []string{"0", "-1", "201", "abc"} {
		t.Run("limit="+raw, func(t *testing.T) {
			response := all.call(t, http.MethodGet, "/v1/leases?limit="+raw, "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("状态码为 %d，期望 400", response.Code)
			}
		})
	}
}

func TestShorten_MovesTheExpiryEarlier(t *testing.T) {
	// REQ-LEASE-002 AC3。
	all := newAPI(t)
	granted := issued(t, all)

	earlier := fixtures.Instant.Add(time.Minute).UTC().Format(time.RFC3339)
	response := all.call(t, http.MethodPost, "/v1/leases/"+granted.ID+"/shorten",
		`{"expires_at":"`+earlier+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.LeaseView
	decodeInto(t, response, &view)
	if view.ExpiresAt >= granted.ExpiresAt {
		t.Errorf("到期时刻从 %s 变成了 %s，没有提前", granted.ExpiresAt, view.ExpiresAt)
	}
}

func TestShorten_ALaterExpiryIsRefused(t *testing.T) {
	// 这个端点不能被用来延长授权。
	all := newAPI(t)
	granted := issued(t, all)

	later := fixtures.Instant.Add(48 * time.Hour).UTC().Format(time.RFC3339)
	response := all.call(t, http.MethodPost, "/v1/leases/"+granted.ID+"/shorten",
		`{"expires_at":"`+later+`"}`)
	if response.Code == http.StatusOK {
		t.Fatalf("把到期时刻推后成功了，正文为 %s", response.Body.String())
	}

	still, err := all.backend.Leases.LeaseByID(t.Context(), granted.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if formatted := formatExpiry(still.ExpiresAt); formatted != granted.ExpiresAt {
		t.Errorf("库里的到期时刻从 %s 变成了 %s", granted.ExpiresAt, formatted)
	}
}

// formatExpiry 与 LeaseView 里的时间格式一致，便于直接与响应里的字符串比较。
func formatExpiry(instant time.Time) string {
	return instant.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func TestShorten_MalformedExpiryIsRefused(t *testing.T) {
	all := newAPI(t)
	granted := issued(t, all)

	response := all.call(t, http.MethodPost, "/v1/leases/"+granted.ID+"/shorten",
		`{"expires_at":"下周三"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("状态码为 %d，期望 400", response.Code)
	}
}

func TestRevoke_ClosesTheLease(t *testing.T) {
	// REQ-LEASE-002：用户可以随时收回。
	all := newAPI(t)
	granted := issued(t, all)

	response := all.call(t, http.MethodDelete, "/v1/leases/"+granted.ID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("状态码为 %d，正文为 %s", response.Code, response.Body.String())
	}

	var view httpapi.LeaseView
	decodeInto(t, response, &view)
	if view.Status != string(lease.StatusRevoked) {
		t.Errorf("状态为 %q，期望 revoked", view.Status)
	}
	assertLeaseCount(t, all, 0)
}

func TestRevoke_AnotherAgentsLeaseIsNotFound(t *testing.T) {
	// 与能力请求同一条规则：看不到的东西与不存在给出同一个答复。
	all := newAPI(t)
	granted := issued(t, all)

	other := newAPIForBackend(t, all.backend, httpapi.Caller{AgentID: "agent-b"})
	response := other.call(t, http.MethodDelete, "/v1/leases/"+granted.ID, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("状态码为 %d，期望 404", response.Code)
	}

	still, err := all.backend.Leases.LeaseByID(t.Context(), granted.ID)
	if err != nil {
		t.Fatalf("读取 Lease 失败：%v", err)
	}
	if still.Status != lease.StatusActive {
		t.Errorf("Lease 被别的 Agent 收回了，状态为 %s", still.Status)
	}
}

func TestRevoke_AnAlreadyClosedLeaseReturns409(t *testing.T) {
	all := newAPI(t)
	granted := issued(t, all)

	if first := all.call(t, http.MethodDelete, "/v1/leases/"+granted.ID, ""); first.Code != http.StatusOK {
		t.Fatalf("第一次收回失败：%d", first.Code)
	}

	second := all.call(t, http.MethodDelete, "/v1/leases/"+granted.ID, "")
	if second.Code != http.StatusConflict {
		t.Fatalf("状态码为 %d，期望 409", second.Code)
	}
}
