package gatewayclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

/*
 * 本机 Gateway 的状态探测。
 *
 * 本包是「出站请求只能从 internal/adapter 发出」的**唯一已登记例外**
 * （架构测试 test/arch/outbound_test.go）。
 *
 * 例外成立的理由，三条缺一不可：连的是本机回环上的自家进程；请求带的是会话令牌
 * 而不是任何外部服务的凭据；不跟随重定向。把它单独成包而不是留在 internal/cli，
 * 是为了让允许清单精确到这一个包 —— 否则将来 opendelo run 在 CLI 里加一次
 * 真正的外部调用，架构测试不会响。
 */

const (
	// Timeout 短到足以让「没在跑」立刻有结论，长到能容忍一次冷启动的握手。
	Timeout = 3 * time.Second
	// maxStatusBody 是状态响应的读取上限，防止对端异常时把内存吃光。
	maxStatusBody = 64 << 10
)

// Probe 向本机 Gateway 要一次状态。
//
// 出站请求被限制在 adapter 包内，那条规则针对的是
// 携带凭据访问外部服务；这里访问的是本机回环上的自家进程，请求不带任何凭据，
// 也不跟随重定向。
func Probe(ctx context.Context, address, sessionToken string) (httpapi.Status, error) {
	endpoint := "http://" + address + "/v1/gateway/status"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return httpapi.Status{}, apperr.Wrap(apperr.CodeInternal, err).WithDetail("无法构造探测请求")
	}
	// 令牌只走请求头：URL 会进 shell 历史、进程列表与访问日志（REQ-API-005）。
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)

	client := &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	response, err := client.Do(request)
	if err != nil {
		return httpapi.Status{}, apperr.Wrap(apperr.CodeGatewayUnavailable, err).
			WithDetail("连不上 " + address + "：Gateway 似乎没有在运行。用 opendelo start 启动它。")
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return httpapi.Status{}, apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail(address + " 上有服务在响应，但状态端点返回 " + strconv.Itoa(response.StatusCode) +
				"。确认该端口上跑的确实是 opendelo。")
	}

	var status httpapi.Status
	if err := json.NewDecoder(io.LimitReader(response.Body, maxStatusBody)).Decode(&status); err != nil {
		return httpapi.Status{}, apperr.Wrap(apperr.CodeGatewayUnavailable, err).
			WithDetail(address + " 上的响应不是预期的状态结构。确认该端口上跑的确实是 opendelo。")
	}
	if status.Status == "" {
		return httpapi.Status{}, apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail(address + " 上的响应缺少状态字段。确认该端口上跑的确实是 opendelo。")
	}
	return status, nil
}
