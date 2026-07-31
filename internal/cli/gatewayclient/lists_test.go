package gatewayclient_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
)

// 零值页大小要在这一层回落。CLI 的 --limit 有自己的默认值，走不到这条分支，
// 而任何直接用本包的调用方都可能传 0 —— 那时发出去的会是一条无界查询。
func TestPageSize_ZeroLimit_FallsBackToTheDefault(t *testing.T) {
	var seen url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query()
		if _, err := w.Write([]byte(`{"items":[],"next_cursor":""}`)); err != nil {
			t.Errorf("写出响应失败：%v", err)
		}
	}))
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	cases := map[string]func() error{
		"identities": func() error {
			_, err := gatewayclient.Identities(t.Context(), address, "token", 0)
			return err
		},
		"leases": func() error {
			_, err := gatewayclient.Leases(t.Context(), address, "token", 0)
			return err
		},
		"audit-events": func() error {
			_, err := gatewayclient.AuditEvents(t.Context(), address, "token",
				gatewayclient.AuditFilter{})
			return err
		},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			seen = nil
			if err := request(); err != nil {
				t.Fatalf("请求失败：%v", err)
			}
			if got := seen.Get("limit"); got != "50" {
				t.Errorf("limit 为 %q，期望回落到默认的 50", got)
			}
		})
	}
}
