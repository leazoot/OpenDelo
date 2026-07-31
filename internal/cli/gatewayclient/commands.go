package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
)

/*
 * CLI 会用到的两个写操作（REQ-CLI-001：connect 与 revoke）。
 *
 * 与 Probe 同属本包的例外范围：连的是本机回环上的自家进程，带的是会话令牌，
 * 不跟随重定向。CLI 里不得出现第二处直接发请求的地方。
 *
 * 网关返回的错误体按 REQ-API-003 的形状解出来原样上抛：把它折成一句
 * 「操作失败」会让用户丢掉 operation_id，而那是去账本里查这次到底怎么了的唯一线索。
 */

// maxBody 是响应体的读取上限，防止对端异常时把内存吃光。
const maxBody = 256 << 10

// ConnectRequest 是建立一个身份所需的一切（REQ-IDENT-001）。
//
// 这里只有**引用**，没有任何能放下明文的字段：凭据明文从不经过 Web API
// （REQ-CRED-001）。
type ConnectRequest struct {
	CredentialReferenceID string `json:"credential_reference_id"`
	AccountLabel          string `json:"account_label"`
	Environment           string `json:"environment"`
	IsDefault             bool   `json:"is_default"`
}

// ConnectIdentity 用一份已登记的凭据引用建立一个身份。
func ConnectIdentity(
	ctx context.Context, address, sessionToken string, connect ConnectRequest,
) (httpapi.IdentityView, error) {
	body, err := json.Marshal(connect)
	if err != nil {
		return httpapi.IdentityView{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法构造连接请求")
	}

	var identity httpapi.IdentityView
	err = call(ctx, address, sessionToken, http.MethodPost, "/v1/identities/connect", body, &identity)
	return identity, err
}

// RevokeLease 收回一条 Lease（REQ-LEASE-002）。
func RevokeLease(
	ctx context.Context, address, sessionToken, leaseID string,
) (httpapi.LeaseView, error) {
	if leaseID == "" {
		return httpapi.LeaseView{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有给出要收回的 Lease")
	}

	var revoked httpapi.LeaseView
	// 路径段转义：ID 来自命令行，不能直接拼进 URL。
	path := "/v1/leases/" + url.PathEscape(leaseID)
	err := call(ctx, address, sessionToken, http.MethodDelete, path, nil, &revoked)
	return revoked, err
}

// RegisterRequest 是一次 Agent 注册（REQ-AGENT-001 的九项身份绑定）。
//
// 全部由 `opendelo run` 如实填写：它是子进程的父进程，这些事实它都看得到，
// 而按威胁模型 Agent 自己说的不算数。
type RegisterRequest struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Version string `json:"version"`

	ExecutableHash string `json:"executable_hash"`
	ExecutablePath string `json:"executable_path"`
	PID            int    `json:"pid"`
	ParentPID      int    `json:"parent_pid"`
	OSUser         string `json:"os_user"`

	DeviceFingerprint string `json:"device_fingerprint"`
	DeviceName        string `json:"device_name"`

	WorkspacePath      string `json:"workspace_path"`
	ProjectFingerprint string `json:"project_fingerprint"`

	StartedAt string `json:"started_at"`
}

// RegisterAgent 注册一个 Agent 并取回本次会话的 Session Key。
//
// 返回体里有一次明文会话凭证。调用方只应把它交给子进程，不得记录、
// 不得写文件、不得回显（对凭据的要求同样适用）。
func RegisterAgent(
	ctx context.Context, address, sessionToken string, registration RegisterRequest,
) (httpapi.RegisteredAgentView, error) {
	body, err := json.Marshal(registration)
	if err != nil {
		return httpapi.RegisteredAgentView{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("无法构造注册请求")
	}

	var registered httpapi.RegisteredAgentView
	err = call(ctx, address, sessionToken, http.MethodPost, "/v1/agents/register", body, &registered)
	return registered, err
}

// DisconnectAgent 结束一个 Agent 会话，并收回它名下绑定会话的 Lease（REQ-CLI-002 AC3）。
func DisconnectAgent(
	ctx context.Context, address, sessionToken, agentID string,
) (httpapi.SessionRevocationView, error) {
	if agentID == "" {
		return httpapi.SessionRevocationView{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("没有给出要结束的会话")
	}

	var revocation httpapi.SessionRevocationView
	// 路径段转义：ID 来自上一次响应，不直接拼进 URL。
	path := "/v1/agents/" + url.PathEscape(agentID) + "/disconnect"
	err := call(ctx, address, sessionToken, http.MethodPost, path, nil, &revocation)
	return revocation, err
}

// call 发出一次请求并把成功响应解进 result。
func call(
	ctx context.Context, address, sessionToken, method, path string,
	body []byte, result any,
) error {
	var payload io.Reader
	if len(body) > 0 {
		payload = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, payload)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, err).WithDetail("无法构造请求")
	}
	// 令牌只走请求头：URL 会进 shell 历史、进程列表与访问日志（REQ-API-005）。
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set(httpapi.HeaderRequestedBy, httpapi.RequestedByConsole)
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	response, err := client.Do(request)
	if err != nil {
		return apperr.Wrap(apperr.CodeGatewayUnavailable, err).
			WithDetail("连不上 " + address + "：Gateway 似乎没有在运行。用 opendelo start 启动它。")
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	decoded, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return apperr.Wrap(apperr.CodeGatewayUnavailable, err).WithDetail("读取响应失败")
	}
	// 任何 2xx 都算成功：创建类端点回 201，其余回 200，而两者对调用方
	// 没有区别。只认 200 的话，新增一个 201 端点会被报成「网关不可用」。
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return gatewayError(decoded, response.StatusCode)
	}
	if err := json.Unmarshal(decoded, result); err != nil {
		return apperr.Wrap(apperr.CodeGatewayUnavailable, err).
			WithDetail("响应不是预期的结构。确认该端口上跑的确实是 opendelo。")
	}
	return nil
}

// gatewayError 把网关的错误体还原成一个带 operation_id 的错误。
//
// 解不出错误体时不假装知道原因，只报状态码 —— 编一个错误码出来会让用户
// 照着一个不存在的原因去排查。
func gatewayError(body []byte, status int) error {
	var envelope struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			OperationID string `json:"operation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Code == "" {
		return apperr.New(apperr.CodeGatewayUnavailable).
			WithDetail("Gateway 返回 " + strconv.Itoa(status) + "，响应体不是预期的错误结构")
	}

	detail := envelope.Error.Code + "：" + envelope.Error.Message
	if envelope.Error.OperationID != "" {
		detail += "（operation_id " + envelope.Error.OperationID + "）"
	}
	return apperr.New(apperr.CodeGatewayUnavailable).WithDetail(detail)
}
