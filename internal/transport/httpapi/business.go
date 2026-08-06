package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/core/trust"
	"github.com/Runcoor/opendelo/internal/credential/localvault"
	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/settings"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * 业务端点的公共部分（REQ-API-001 的前 11 个）。
 *
 * 这一层只做协议转换：解析、取调用方、调用 core、
 * 序列化。这里没有一处在判断「是否允许」—— 那全在 core/decision 与 core/approval。
 * 唯一形似判断的是「这个调用方能不能看到这条记录」，那是可见性不是授权，
 * 而且答案只有一个来源：Caller.maySee。
 */

const (
	// defaultLimit 与 maxLimit 是列表端点的分页边界。
	defaultLimit = 50
	maxLimit     = 200
	// maxBodyBytes 是请求正文上限。JSON 请求体都很小，给足量即可 ——
	// 不设上限等于让任何本机进程用一次请求把内存吃光。
	maxBodyBytes = 1 << 20
)

// Services 是业务端点依赖的核心组件，全部必填。
//
// 端点持有的是 core 侧的 Manager 与仓储接口，不是数据库 —— 数据库访问只经 store
// Submissions 把一条已落库的能力请求推进决策链路。
//
// 定义成端口而不是直接调 pipeline.Handle：装配 `pipeline.Inputs` 要读 Adapter
// 的能力声明，而 transport 不得 import adapter。
// 实现在 `internal/orchestration`，那里是唯一同时看得见两边的地方。
//
// 三个接入面共用那一个实现：`pipeline.Inputs` 少填一项不会报错，只会让风险算得
// 偏低，而两份装配里迟早有一份漏掉其中一项。
type Submissions interface {
	Decide(ctx context.Context, request pipeline.CapabilityRequest) (pipeline.Result, error)
}

// Declarer 保证一个服务的 Adapter 声明在库里。
//
// 决策链路读的是数据库里的声明，而 Adapter 在编译期注册 —— 两者之间需要一座桥。
// 桥搭在连接流程上而不是启动时：启动时把全部 Adapter 写进去并启用，
// 等于替用户「连接」了几个他没配过凭据的服务。
// 只返回 error：连接流程要的是「确保它在」，不需要声明内容。
// 返回 adapters.Declaration 会把 adapter 包拖进 transport 的依赖里，
// 而依赖方向只允许 core → adapter（架构测试强制）。
type Declarer interface {
	EnsureDeclared(ctx context.Context, service string) error
}

// Capabilities 回答「这个服务一共声明过哪些操作」。
//
// 卷宗的「仍然关闭的权限」要说清这次放行之外还有什么没给（REQ-APPROVAL-001 AC4），
// 而那份清单只有 Adapter 的能力声明知道。同样定义成端口：transport 不得
// import adapter，实现是 adapter/registry 的注册表。
type Capabilities interface {
	Operations(service string) []string
	// Services 是已声明 Adapter 的服务名。连接身份时据此拒绝一个没有 Adapter
	// 的服务：那样的身份匹配上了也无从执行，而它在界面上看起来是连好的。
	Services() []string
}

type Services struct {
	Pipeline *pipeline.Pipeline
	// Submissions 是 POST /v1/capability-requests 背后的决策入口。
	Submissions Submissions
	// Capabilities 服务卷宗上的「仍然关闭的权限」。
	Capabilities Capabilities
	// Declarer 在连接身份时把该服务的 Adapter 声明写进库（R-24）。
	// 定义成端口：transport 不得 import adapter。
	Declarer   Declarer
	Requests   pipeline.CapabilityRequestRepository
	Decisions  decision.DecisionRepository
	Approvals  *approval.Manager
	Leases     *lease.Manager
	Memories   *trust.Manager
	Identities matcher.IdentityRepository
	// Credentials 只用来读引用元数据与探测健康状态。明文的唯一出口是
	// Registry.Fetch，其返回类型 secret.Value 在本包不可见（架构测试强制）。
	Credentials *credentials.Registry
	// Ledger 是账本的**只读**面：接入面没有理由能写它，也没有理由能删它。
	Ledger audit.Reader
	// Agents 与 AgentAuth 服务 Identities 页面的 Agents 列与信任确认。
	Agents    agentauth.AgentRepository
	AgentAuth *agentauth.Service
	// Preferences 是运行期可改的偏好（REQ-PREF-001）。
	Preferences *settings.Store
	// Config 是启动时读进来的那些设置，本包只读不写：改它们要重启。
	Config config.Config
	// Vault 为空表示这台 Gateway 没有配置本地保险库，两个端点返回 not_implemented。
	Vault *localvault.Vault
	// Events 为空时由 NewBusinessHandler 自建一个。传进来是为了让进程退出时
	// 能把全部订阅关掉，也让用例能看见订阅者数量。
	Events *Broker
	Clock  clock.Clock
	IDs    *ulid.Generator
}

func (s Services) validate() error {
	missing := ""
	switch {
	case s.Pipeline == nil:
		missing = "Pipeline"
	case s.Submissions == nil:
		missing = "Submissions"
	case s.Capabilities == nil:
		missing = "Capabilities"
	case s.Requests == nil:
		missing = "Requests"
	case s.Decisions == nil:
		missing = "Decisions"
	case s.Approvals == nil:
		missing = "Approvals"
	case s.Leases == nil:
		missing = "Leases"
	case s.Memories == nil:
		missing = "Memories"
	case s.Identities == nil:
		missing = "Identities"
	case s.Credentials == nil:
		missing = "Credentials"
	case s.Declarer == nil:
		missing = "Declarer"
	case s.Ledger == nil:
		missing = "Ledger"
	case s.Agents == nil:
		missing = "Agents"
	case s.AgentAuth == nil:
		missing = "AgentAuth"
	case s.Preferences == nil:
		missing = "Preferences"
	case s.Clock == nil:
		missing = "Clock"
	case s.IDs == nil:
		missing = "IDs"
	}
	if missing != "" {
		return apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("httpapi.Services." + missing + " 未提供")
	}
	return nil
}

// endpoints 持有业务端点的依赖。
type endpoints struct {
	services Services
	events   *Broker
	logger   *slog.Logger
}

// newEndpoints 构造一组端点，并保证广播器非空。
//
// 两条装配路径（httpapi.New 与 NewBusinessHandler）都必须经过这里。
// 直接写 &endpoints{...} 时漏掉 events 不会有任何编译错误，而后果是
// **第一次审批决定就让整个进程 panic** —— 决策端点走完之后要广播一条事件，
// 那时 e.events 是 nil。这个坑踩过一次（见 events_broker_test.go 的回归用例）。
func newEndpoints(services Services, broker *Broker, logger *slog.Logger) *endpoints {
	if broker == nil {
		broker = NewBroker(logger)
	}
	return &endpoints{services: services, events: broker, logger: logger}
}

// statusFor 是错误码到 HTTP 状态码的完整映射。
//
// 一张字面表，且由用例断言它覆盖 apperr.All() 的每一个码：新增错误码时必须
// 在这里给它一个状态，而不是掉进某个 default 里变成 500 —— 那会让
// 「这次为什么失败」在网络层丢失。
var statusFor = map[apperr.Code]int{
	apperr.CodeInvalidRequest:                 http.StatusBadRequest,
	apperr.CodeUnauthenticated:                http.StatusUnauthorized,
	apperr.CodeForbidden:                      http.StatusForbidden,
	apperr.CodeNotFound:                       http.StatusNotFound,
	apperr.CodeConflict:                       http.StatusConflict,
	apperr.CodeInternal:                       http.StatusInternalServerError,
	apperr.CodeNotImplemented:                 http.StatusNotImplemented,
	apperr.CodeInvalidConfiguration:           http.StatusInternalServerError,
	apperr.CodeAgentIdentityUnverifiable:      http.StatusUnauthorized,
	apperr.CodeSessionExpired:                 http.StatusUnauthorized,
	apperr.CodeCapabilityNotOffered:           http.StatusForbidden,
	apperr.CodeApprovalTimeout:                http.StatusRequestTimeout,
	apperr.CodeCredentialNotAuthorized:        http.StatusForbidden,
	apperr.CodeProviderUnavailable:            http.StatusServiceUnavailable,
	apperr.CodeProviderNotSupportedOnPlatform: http.StatusServiceUnavailable,
	apperr.CodeProviderLockedTimeout:          http.StatusGatewayTimeout,
	apperr.CodeVaultLocked:                    http.StatusServiceUnavailable,
	apperr.CodePathNotAllowed:                 http.StatusForbidden,
	apperr.CodeAdapterTimeout:                 http.StatusGatewayTimeout,
	apperr.CodeGatewayUnavailable:             http.StatusServiceUnavailable,
}

// StatusForCode 返回某个错误码对应的 HTTP 状态码，未登记时报告 false。
// 导出只为让用例逐码核对映射表的完整性。
func StatusForCode(code apperr.Code) (int, bool) {
	status, known := statusFor[code]
	return status, known
}

// fail 把核心层的错误写成 REQ-API-003 的错误体。
//
// 认不出的错误按 500 处理：状态码猜低了会让调用方以为「换个参数再试就行」，
// 而它其实是服务端的问题。
func (e *endpoints) fail(w http.ResponseWriter, r *http.Request, err error) {
	if fields := fieldsOf(err); len(fields) > 0 {
		writeValidationError(w, r, e.logger, err, fields...)
		return
	}

	status := http.StatusInternalServerError
	var typed *apperr.Error
	if errors.As(err, &typed) {
		if mapped, known := statusFor[typed.Code()]; known {
			status = mapped
		}
	}
	writeError(w, r, e.logger, status, err)
}

// notFound 是「看不到」与「不存在」共用的答复（REQ-CAP-002 AC2）。
//
// 两种情况必须给出**同一个**答复：区分开来就等于告诉调用方这个 id 存在，
// 只是不属于它。
func notFound(kind, id string) error {
	return apperr.New(apperr.CodeNotFound).WithDetail("找不到" + kind + " " + id)
}

// decodeBody 解析请求正文。
//
// 拒绝未知字段：请求里带了本端点读不懂的东西时，与其忽略它继续，不如告诉调用方
// 它以为生效的那个字段其实没生效。越权字段（scope、permissions）由各端点
// 单独处理 —— 那要记审计而不是报错（REQ-SCOPE-002）。
func decodeBody(r *http.Request, into any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return apperr.Wrap(apperr.CodeInvalidRequest, err).WithDetail("请求正文无法解析")
	}
	return nil
}

// limitFrom 取分页条数。
//
// 认不出的取值直接拒绝而不是退回默认值：调用方写了 limit=abc 时它想要的
// 显然不是 50 条，静默换一个数字会让分页看起来「有时候不生效」。
func limitFrom(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, nil
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > maxLimit {
		return 0, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("limit 必须是 1 到 " + strconv.Itoa(maxLimit) + " 之间的整数")
	}
	return limit, nil
}

// pathID 取路径里的 id 段。
func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if id == "" {
		return "", apperr.New(apperr.CodeInvalidRequest).WithDetail("路径里没有 id")
	}
	return id, nil
}

// decisionFor 读取一个请求的决策，还没有决策时返回 nil。
//
// 「还没决策」不是错误：请求刚落库、正在解析时本来就没有决策记录。
func (e *endpoints) decisionFor(
	ctx context.Context, requestID string,
) (*DecisionView, error) {
	record, err := e.services.Decisions.DecisionByRequestID(ctx, requestID)
	if err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	view := decisionView(record)
	return &view, nil
}
