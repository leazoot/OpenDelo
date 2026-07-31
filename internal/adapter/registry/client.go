package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * Adapter 的共享出站通道（REQ-ADAPTER-005、REQ-ADAPTER-008）。
 *
 * 四条约束都在这里成立，因此每个 Adapter 不必各自记得一遍：
 *
 *  1. **独立的 http.Client + 显式超时**。不用 http.DefaultClient ——
 *     它没有超时，一个不回包的外部服务就能把请求挂到天荒地老。
 *  2. **不跟随跨主机重定向**。跟过去就等于把注入的凭据送到另一台主机。
 *  3. **响应体有上限**。外部服务返回多大就读多大，等于把内存交给对方决定。
 *  4. **只有幂等操作允许重试**。非幂等操作超时后不再发第二次请求 ——
 *     超时不等于没执行，重试可能创建出第二个 PR。
 */

const (
	// DefaultTimeout 是单次出站请求的上限（REQ-ADAPTER-008，假设值）。
	DefaultTimeout = 30 * time.Second
	// DefaultMaxResponseBytes 是响应体上限。
	//
	// 取 4 MiB：GitHub 的列表响应可以到几百 KB，留出余量；再大的响应对
	// 「返回给 Agent 的只是脱敏后的少数字段」这个用法来说没有意义。
	DefaultMaxResponseBytes int64 = 4 << 20
	// DefaultBackoff 是第一次重试前的等待，之后指数增长。
	DefaultBackoff = 200 * time.Millisecond
	// MaxRetries 是重试次数上限（REQ-ADAPTER-008）。只对幂等操作生效。
	MaxRetries = 2
	// maxRedirects 是同主机内允许的跳转次数。
	maxRedirects = 5
)

// Waiter 等待一段时间，或在 ctx 取消时提前返回错误。用例用它避免真实等待。
type Waiter func(ctx context.Context, delay time.Duration) error

// ClientOptions 是 Client 的配置。
type ClientOptions struct {
	// BaseURL 必须是带 http 或 https 的绝对地址。
	BaseURL string
	// Timeout 为零时用 DefaultTimeout。
	Timeout time.Duration
	// MaxResponseBytes 为零时用 DefaultMaxResponseBytes。
	MaxResponseBytes int64
	// Backoff 为零时用 DefaultBackoff。
	Backoff time.Duration
	// Transport 为空时用 http.Transport 的默认值。用例用它接管出站。
	Transport http.RoundTripper
	// Wait 为空时真实等待。
	Wait Waiter
}

// Client 是一个 Adapter 的出站通道。每个 Adapter 一个，不共用。
type Client struct {
	baseURL  *url.URL
	http     *http.Client
	maxBytes int64
	backoff  time.Duration
	wait     Waiter
}

// Request 是一次出站请求。
//
// Credential 是明文，只在本包内活到请求发出为止；调用方负责 Zero。
type Request struct {
	// Capability 提供方法、路径模板与幂等性。
	Capability Capability
	// Path 是本次的实际路径，必须匹配 Capability.Path。
	Path string
	// Query 是查询参数。
	Query url.Values
	// Body 是请求体，读操作为空。
	Body []byte
	// AuthScheme 决定凭据注入到哪里。
	AuthScheme AuthScheme
	// AuthHeader 是 AuthHeader 方案下的请求头名。
	AuthHeader string
	// Credential 是凭据明文。AuthNone 时可以是零值。
	Credential secret.Value
	// OperationID 随错误一起返回，供账本追溯（REQ-ADAPTER-008 AC3）。
	OperationID string
}

// Response 是一次出站请求的结果。
//
// Body 是**未脱敏**的原始响应体，只允许在本包内流转；返回给 Agent 之前
// 必须经 Redact（REQ-ADAPTER-007）。
type Response struct {
	StatusCode int
	Body       []byte
	// Attempts 是实际发出的请求次数，写入审计（REQ-ADAPTER-008 AC2）。
	Attempts int
}

// NewClient 构造一个 Adapter 专用的出站通道。
func NewClient(options ClientOptions) (*Client, error) {
	base, err := url.Parse(options.BaseURL)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidConfiguration, err).
			WithDetail("Adapter 的 Base URL 解析失败")
	}
	if base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("Adapter 的 Base URL 必须是 http 或 https 的绝对地址：" + options.BaseURL)
	}

	client := &Client{
		baseURL:  base,
		maxBytes: options.MaxResponseBytes,
		backoff:  options.Backoff,
		wait:     options.Wait,
	}
	if client.maxBytes <= 0 {
		client.maxBytes = DefaultMaxResponseBytes
	}
	if client.backoff <= 0 {
		client.backoff = DefaultBackoff
	}
	if client.wait == nil {
		client.wait = waitReal
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client.http = &http.Client{
		Timeout:       timeout,
		Transport:     options.Transport,
		CheckRedirect: client.checkRedirect,
	}
	return client, nil
}

// checkRedirect 拒绝跨主机跳转（REQ-ADAPTER-005 AC3）。
//
// 跟过去就等于把注入的凭据送到另一台主机，而那台主机是外部服务指定的。
func (c *Client) checkRedirect(request *http.Request, via []*http.Request) error {
	if request.URL.Host != c.baseURL.Host {
		return apperr.New(apperr.CodePathNotAllowed).
			WithDetail("拒绝跨主机重定向：" + request.URL.Host)
	}
	if len(via) >= maxRedirects {
		return apperr.New(apperr.CodePathNotAllowed).
			WithDetail("重定向次数超过上限")
	}
	return nil
}

// Do 发出一次请求，必要时按幂等性重试。
//
// 路径先过白名单：不匹配就**不发出任何请求**（REQ-ADAPTER-005 AC1）。
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if !pathAllowed(request.Capability.Path, request.Path) {
		return Response{}, apperr.New(apperr.CodePathNotAllowed).
			WithDetail("路径 " + request.Path + " 不在 " +
				request.Capability.Operation + " 声明的范围内").
			WithOperationID(request.OperationID)
	}

	attempts := 1
	if request.Capability.Idempotency == Idempotent {
		attempts += MaxRetries
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			delay := c.backoff << (attempt - 2)
			if err := c.wait(ctx, delay); err != nil {
				return Response{}, apperr.Wrap(apperr.CodeGatewayUnavailable, err).
					WithDetail("重试等待被取消").
					WithOperationID(request.OperationID)
			}
		}

		response, err := c.attempt(ctx, request)
		response.Attempts = attempt
		if err == nil && !retryableStatus(response.StatusCode) {
			return response, nil
		}
		if err == nil {
			lastErr = nil
			if attempt == attempts {
				return response, nil
			}
			continue
		}
		lastErr = err
		if !retryableError(err) {
			return Response{Attempts: attempt}, err
		}
	}
	return Response{Attempts: attempts}, lastErr
}

// attempt 发出一次请求并读回有上限的响应体。
func (c *Client) attempt(ctx context.Context, request Request) (result Response, err error) {
	target := *c.baseURL
	target.Path = strings.TrimSuffix(c.baseURL.Path, "/") + request.Path
	target.RawQuery = request.Query.Encode()

	var body io.Reader
	if len(request.Body) > 0 {
		body = bytes.NewReader(request.Body)
	}

	outbound, err := http.NewRequestWithContext(
		ctx, request.Capability.Method, target.String(), body)
	if err != nil {
		return Response{}, apperr.Wrap(apperr.CodeGatewayUnavailable, err).
			WithDetail("请求构造失败").
			WithOperationID(request.OperationID)
	}
	if len(request.Body) > 0 {
		outbound.Header.Set("Content-Type", "application/json")
	}
	if err = injectCredential(outbound, request); err != nil {
		return Response{}, err
	}

	inbound, err := c.http.Do(outbound)
	if err != nil {
		return Response{}, translateTransportError(err, request.OperationID)
	}
	defer func() {
		if closeErr := inbound.Body.Close(); closeErr != nil && err == nil {
			err = apperr.Wrap(apperr.CodeGatewayUnavailable, closeErr).
				WithDetail("关闭响应体失败").
				WithOperationID(request.OperationID)
			result = Response{}
		}
	}()

	// 多读一个字节用来判断「是不是被截断了」：截断后的 JSON 解析出来是残缺数据，
	// 而残缺数据会被当成外部服务的真实答复。
	payload, err := io.ReadAll(io.LimitReader(inbound.Body, c.maxBytes+1))
	if err != nil {
		return Response{}, translateTransportError(err, request.OperationID)
	}
	if int64(len(payload)) > c.maxBytes {
		return Response{}, apperr.New(apperr.CodeInternal).
			WithDetail("外部服务的响应体超过上限，已放弃").
			WithOperationID(request.OperationID)
	}

	return Response{StatusCode: inbound.StatusCode, Body: payload}, nil
}

// injectCredential 把明文放进请求头。
//
// 注入只发生在这里。凭据不进 URL、不进 Query、
// 不进请求体 —— 那三处都会被外部服务写进它自己的访问日志。
func injectCredential(outbound *http.Request, request Request) error {
	switch request.AuthScheme {
	case AuthNone:
		return nil
	case AuthBearer:
		outbound.Header.Set("Authorization", "Bearer "+string(request.Credential.Reveal()))
		return nil
	case AuthHeader:
		if strings.TrimSpace(request.AuthHeader) == "" {
			return apperr.New(apperr.CodeInvalidConfiguration).
				WithDetail("自定义请求头方案没有声明头名").
				WithOperationID(request.OperationID)
		}
		outbound.Header.Set(request.AuthHeader, string(request.Credential.Reveal()))
		return nil
	default:
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("认不出的凭据注入方式：" + string(request.AuthScheme)).
			WithOperationID(request.OperationID)
	}
}

// translateTransportError 把传输层错误折成对外的错误码。
//
// 超时单独一类：REQ-ADAPTER-008 AC3 要求返回 adapter_timeout 并保留操作 ID。
func translateTransportError(err error, operationID string) error {
	var typed *apperr.Error
	if errors.As(err, &typed) {
		return typed.WithOperationID(operationID)
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return apperr.Wrap(apperr.CodeAdapterTimeout, err).
			WithDetail("外部服务在超时时间内没有响应").
			WithOperationID(operationID)
	}
	return apperr.Wrap(apperr.CodeGatewayUnavailable, err).
		WithDetail("出站请求失败").
		WithOperationID(operationID)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// retryableError 只认超时与网络失败。
//
// 路径不允许、跨主机重定向、配置错误重试多少次都是同一个结果，
// 重试只会让账本上多出几条没有意义的记录。
func retryableError(err error) bool {
	return apperr.Is(err, apperr.CodeAdapterTimeout) ||
		apperr.Is(err, apperr.CodeGatewayUnavailable)
}

// retryableStatus 认为 429 与 5xx 值得再试一次。
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// pathAllowed 用声明里的模板匹配实际路径（端点白名单）。
//
// 模板里的 {name} 匹配一个非空、不含斜杠的段。段数必须相等 ——
// 允许多出来的段就等于允许 /repos/{owner}/{repo} 命中 /repos/x/y/collaborators。
// pathAllowed 是端点白名单的判定。取值本身在出站时用不上，只看匹不匹配。
func pathAllowed(template, actual string) bool {
	_, matched := MatchPath(template, actual)
	return matched
}

func waitReal(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
