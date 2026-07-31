package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 访问日志的用例（REQ-PROXY-001 AC2）。
 *
 * 这里刻意把哨兵放进 Agent 送来的 Authorization —— 只有真的有值经过，
 * 「值被替换为 [redacted]」才是一句可以被证伪的话。用一个恒为空的头去测，
 * 用例会因为「压根没有值」而通过，那和没测一样。
 */

func logLines(t *testing.T, h *harness) []map[string]any {
	t.Helper()

	lines := make([]map[string]any, 0, 2)
	for _, raw := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("日志不是合法 JSON：%v\n%s", err, raw)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestLogAccess_AgentSuppliedCredentials_AreRedacted(t *testing.T) {
	h := newHarness(t)
	request := proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil)
	request.Header.Set("Authorization", "Bearer "+sentinel.SentinelToken)
	request.Header.Set(sessionHeader, sentinel.SentinelAPIKey)

	h.do(t, request)

	output := h.logs.String()
	for _, value := range sentinel.All() {
		if strings.Contains(output, value) {
			t.Errorf("访问日志里出现了哨兵 %s：\n%s", value, output)
		}
	}

	lines := logLines(t, h)
	if len(lines) != 1 {
		t.Fatalf("写出了 %d 条访问日志，期望一条", len(lines))
	}
	for _, key := range []string{"authorization", "proxy_authorization"} {
		if lines[0][key] != logging.Redacted {
			t.Errorf("%s 字段为 %v，期望 %q", key, lines[0][key], logging.Redacted)
		}
	}
}

func TestLogAccess_TheScanWouldCatchALeak(t *testing.T) {
	// 反向对照：换一个不在脱敏词表里的 key，同一个哨兵就会原样出现在输出里。
	// 没有这条，上面那个用例可能只是因为哨兵从来没进过日志而通过。
	h := newHarness(t)
	logger := logging.New(logging.Options{Level: slog.LevelDebug, Writer: h.logs})
	logger.Info("对照", slog.String("host", sentinel.SentinelToken))

	if !strings.Contains(h.logs.String(), sentinel.SentinelToken) {
		t.Fatal("非敏感字段里的哨兵没有出现在输出里，说明扫描根本看不到日志内容")
	}
}

func TestLogAccess_QueryStringStaysOutOfTheLog(t *testing.T) {
	// 记 URL 时只留 path。有些服务把令牌
	// 放在 query 里，那不是本网关能预料的。
	h := newHarness(t)
	h.do(t, proxyRequest(t, http.MethodGet,
		"http://api.github.com/repos/acme/console?access_token="+sentinel.SentinelToken, nil))

	lines := logLines(t, h)
	if len(lines) != 1 {
		t.Fatalf("写出了 %d 条访问日志", len(lines))
	}
	if path := lines[0]["path"]; path != "/repos/acme/console" {
		t.Errorf("path 字段为 %v", path)
	}
	if strings.Contains(h.logs.String(), "access_token") {
		t.Errorf("查询串进了日志：\n%s", h.logs.String())
	}
}

func TestLogAccess_EachOutcomeIsRecorded(t *testing.T) {
	cases := map[string]struct {
		prepare func(*harness)
		request func(*testing.T) *http.Request
		outcome string
		status  float64
	}{
		"放行": {
			prepare: func(*harness) {},
			request: func(t *testing.T) *http.Request {
				return proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil)
			},
			outcome: "forwarded", status: float64(http.StatusOK),
		},
		"无 Lease": {
			prepare: func(h *harness) {
				h.leases.err = apperr.New(apperr.CodeCredentialNotAuthorized)
			},
			request: func(t *testing.T) *http.Request {
				return proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil)
			},
			outcome: "refused", status: float64(http.StatusForbidden),
		},
		"隧道": {
			prepare: func(*harness) {},
			request: func(t *testing.T) *http.Request {
				request := proxyRequest(t, http.MethodConnect, "http://api.github.com:443", nil)
				request.Host = "api.github.com:443"
				return request
			},
			outcome: "tunnel_refused", status: float64(http.StatusForbidden),
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			testCase.prepare(h)
			h.do(t, testCase.request(t))

			lines := logLines(t, h)
			if len(lines) != 1 {
				t.Fatalf("写出了 %d 条访问日志，期望一条", len(lines))
			}
			if lines[0]["outcome"] != testCase.outcome {
				t.Errorf("outcome 为 %v，期望 %q", lines[0]["outcome"], testCase.outcome)
			}
			if lines[0]["status"] != testCase.status {
				t.Errorf("status 为 %v，期望 %v", lines[0]["status"], testCase.status)
			}
		})
	}
}

func TestLogAccess_ForwardedRequest_CarriesTheServiceAndLease(t *testing.T) {
	// 账本靠 operation_id 串起来，运维日志靠这几个字段回答「刚才那条请求走的是哪条授权」。
	h := newHarness(t)
	h.do(t, proxyRequest(t, http.MethodGet, "http://api.github.com/repos/acme/console", nil))

	line := logLines(t, h)[0]
	for key, expected := range map[string]string{
		"service":   "github",
		"operation": "read_repository",
		"lease_id":  "lease_1",
		"host":      "api.github.com",
	} {
		if line[key] != expected {
			t.Errorf("%s 字段为 %v，期望 %q", key, line[key], expected)
		}
	}
}
