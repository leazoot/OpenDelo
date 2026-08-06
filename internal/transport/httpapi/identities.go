package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Runcoor/opendelo/internal/core/matcher"
	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * Identity API（PRD §27、REQ-IDENT-001/004）。
 *
 * 这四个端点全程不碰凭据明文：连接给的是一份**引用**（provider + item + field），
 * 校验走的是 Provider 的探测，返回体里没有任何字段能放下一个 Token。
 * Agent 一律看不到这些端点 —— 身份是人的配置面。
 */

// connectBody 是 POST /v1/identities/connect 的请求体。
//
// 收的是一份凭据的**坐标**，不是凭据（REQ-CRED-001）：ProviderItemRef 说的是
// 「1Password 里那个叫 GitHub Bot 的条目」，Field 说的是「那个条目的 token 字段」。
// 凭这三项无法离线还原出任何 Secret，明文永远来自 Provider 的实时取用。
//
// 这里没有一个字段能装下凭据本身，而 decodeBody 拒绝未知字段，
// 因此请求里塞一个 token 进来只会得到 400。
type connectBody struct {
	// ProviderKind 是本期实现的三种来源之一（REQ-CRED-006 AC1）。
	ProviderKind string `json:"provider_kind"`
	// ProviderLabel 区分同一种类下的多个来源（两个 1Password 账号）。
	ProviderLabel string `json:"provider_label"`
	// ProviderItemRef 是条目在来源内部的坐标，形式由各 Provider 自己定义。
	ProviderItemRef string `json:"provider_item_ref"`
	// Field 是那个条目里的哪个字段。
	Field   string `json:"field"`
	Service string `json:"service"`

	AccountLabel string `json:"account_label"`
	// Environment 是 production 或 non-production。
	Environment string `json:"environment"`
	IsDefault   bool   `json:"is_default"`
}

// listIdentities 返回全部身份，服务 Identities 页面的关系视图。
func (e *endpoints) listIdentities(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "身份"); err != nil {
		return
	}

	limit, err := limitFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	identities, err := e.services.Pipeline.Identities(r.Context(), limit)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	items := make([]IdentityView, 0, len(identities))
	for _, identity := range identities {
		items = append(items, identityView(identity))
	}

	// 连接表单要知道有哪些服务可选。放在这一份响应里而不是新开一个端点：
	// 「这台 Gateway 认得哪些服务」与「现在连着哪些身份」是同一页要回答的事，
	// 而端点清单由 REQ-API-001 固定（`.claude/rules/backend.md` §4.1）。
	services := e.services.Capabilities.Services()
	slices.Sort(services)

	writeJSON(w, r, e.logger, http.StatusOK, identityListEnvelope{
		Items:               items,
		NextCursor:          "",
		ConnectableServices: services,
	})
}

// identityListEnvelope 是 GET /v1/identities 的响应。
//
// 比通用的 listEnvelope 多一项：可连接的服务清单。它不是身份的属性，
// 而是这台 Gateway 的能力 —— 界面据此把「服务」做成下拉而不是让用户猜着填。
type identityListEnvelope struct {
	Items      []IdentityView `json:"items"`
	NextCursor string         `json:"next_cursor"`
	// ConnectableServices 是已声明 Adapter 的服务名，按字母序。
	// 没有 Adapter 的服务连上了也执行不了，因此不出现在这里。
	ConnectableServices []string `json:"connectable_services"`
}

// connectIdentity 登记一份凭据引用并用它建立一个身份（REQ-CRED-002 AC1）。
//
// 引用取不到就不建：一个取不到凭据的身份匹配上了也执行不了，
// 那是执行期才会暴露的失败，与 Fail Closed 相悖。
func (e *endpoints) connectIdentity(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "身份"); err != nil {
		return
	}

	var body connectBody
	if err := decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	environment, err := body.validate(e.services.Capabilities.Services())
	if err != nil {
		e.fail(w, r, err)
		return
	}

	providerID, err := e.services.IDs.NewID()
	if err != nil {
		e.fail(w, r, err)
		return
	}
	referenceID, err := e.services.IDs.NewID()
	if err != nil {
		e.fail(w, r, err)
		return
	}

	reference, err := e.services.Credentials.RegisterReference(r.Context(),
		credentials.Registration{
			ProviderID:    providerID,
			ReferenceID:   referenceID,
			Kind:          credentials.ProviderKind(body.ProviderKind),
			ProviderLabel: body.ProviderLabel,
			ItemRef:       body.ProviderItemRef,
			Field:         body.Field,
			Service:       body.Service,
			AccountLabel:  body.AccountLabel,
		})
	if err != nil {
		e.fail(w, r, err)
		return
	}

	// 声明排在凭据登记**之后**：连接没成立时不留下任何一行。
	// 一份 enabled 的声明会让这个服务出现在工具清单里，而用户那次连接是失败的
	// —— 那正是「替用户连接了一个他没配过凭据的服务」（R-24 的同一条理由）。
	if declareErr := e.services.Declarer.EnsureDeclared(r.Context(), reference.Service); declareErr != nil {
		e.fail(w, r, declareErr)
		return
	}

	id, err := e.services.IDs.NewID()
	if err != nil {
		e.fail(w, r, err)
		return
	}
	now := e.services.Clock.Now()

	created, err := e.services.Identities.CreateIdentity(r.Context(), matcher.Identity{
		ID:                    id,
		Service:               reference.Service,
		AccountLabel:          body.AccountLabel,
		Environment:           environment,
		IsDefault:             body.IsDefault,
		Status:                matcher.StatusOK,
		CredentialReferenceID: reference.ID,
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		e.fail(w, r, err)
		return
	}
	writeJSON(w, r, e.logger, http.StatusCreated, identityView(created))
}

// validate 校验请求体并返回环境取值。
//
// 校验全部在这一层完成，进入 core 与 credential 的数据已是合法类型。
// 服务名虽然由这里收，但落到身份上的那一个取自登记后的引用 ——
// 两处各存一份迟早会出现「引用指向 GitHub、身份自称 Cloudflare」的记录。
func (b connectBody) validate(services []string) (matcher.Environment, error) {
	switch {
	case strings.TrimSpace(b.ProviderKind) == "":
		return "", missingFields("provider_kind")
	case strings.TrimSpace(b.ProviderLabel) == "":
		return "", missingFields("provider_label")
	case strings.TrimSpace(b.ProviderItemRef) == "":
		return "", missingFields("provider_item_ref")
	case strings.TrimSpace(b.Field) == "":
		return "", missingFields("field")
	case strings.TrimSpace(b.Service) == "":
		return "", missingFields("service")
	case strings.TrimSpace(b.AccountLabel) == "":
		return "", missingFields("account_label")
	}

	// 认不出的来源种类在这里就挡下，不进 credential 包：那一层只知道
	// 「这个种类有没有登记过」，答不出「它是不是本期实现的三种之一」。
	if !slices.Contains(credentials.Implemented(), credentials.ProviderKind(b.ProviderKind)) {
		return "", invalidField("provider_kind",
			"provider_kind 只能是 "+strings.Join(kindNames(), "、"))
	}

	// 没有 Adapter 的服务连接了也执行不了，而界面上它看起来是连好的。
	if !slices.Contains(services, b.Service) {
		return "", invalidField("service",
			"service 必须是已声明 Adapter 的服务："+strings.Join(services, "、"))
	}

	environment := matcher.Environment(b.Environment)
	if environment != matcher.EnvironmentProduction &&
		environment != matcher.EnvironmentNonProduction {
		return "", invalidField("environment",
			"environment 只能是 production 或 non-production")
	}
	return environment, nil
}

func kindNames() []string {
	kinds := credentials.Implemented()
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return names
}

// verifyIdentity 探测这个身份背后的凭据现在还能不能用（REQ-CRED-005）。
//
// 探测不通不是错误而是探测的**结论**：身份转为「需要检查」，自动授权暂停，
// 下一次请求进入审批（REQ-IDENT-004 AC1）。
func (e *endpoints) verifyIdentity(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "身份"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	identity, err := e.services.Pipeline.Identity(r.Context(), id)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	probed, err := e.services.Credentials.Probe(r.Context(), identity.CredentialReferenceID)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	status := matcher.StatusNeedsReview
	if probed.HealthStatus == credentials.HealthOK {
		status = matcher.StatusOK
	}
	updated, err := e.services.Identities.SetIdentityStatus(
		r.Context(), id, status, e.services.Clock.Now())
	if err != nil {
		e.fail(w, r, err)
		return
	}

	writeJSON(w, r, e.logger, http.StatusOK, verificationEnvelope{
		Identity: identityView(updated),
		Health:   string(probed.HealthStatus),
	})
}

// disconnectIdentity 断开一个身份并级联收回它名下的授权（REQ-IDENT-001 AC2）。
func (e *endpoints) disconnectIdentity(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "身份"); err != nil {
		return
	}

	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	revocation, err := e.services.Pipeline.DisconnectIdentity(
		r.Context(), id, logging.OperationIDFrom(r.Context()))
	if err != nil {
		e.fail(w, r, err)
		return
	}

	writeJSON(w, r, e.logger, http.StatusOK, revocationEnvelope{
		Identity:            identityView(revocation.Identity),
		RevokedLeases:       len(revocation.RevokedLeases),
		InvalidatedMemories: len(revocation.InvalidatedMemories),
	})
}

// refuseAgent 挡下 Agent 对配置面的访问（REQ-DECIDE-004）。
//
// 写出响应后返回非 nil，调用方据此停止处理。
func (e *endpoints) refuseAgent(w http.ResponseWriter, r *http.Request, face string) error {
	if !callerFrom(r.Context()).IsAgent() {
		return nil
	}

	refusal := apperr.New(apperr.CodeForbidden).
		WithDetail("Agent 不能访问" + face + "端点")
	e.fail(w, r, refusal)
	return refusal
}

func missingFields(names ...string) error {
	return invalidField(strings.Join(names, ","),
		"缺少必填字段 "+strings.Join(names, "、"))
}

// fieldError 是一个能说清「哪个字段不对」的校验错误。
//
// 字段名不能只写进 detail：detail 永远不进对外响应（那正是凭据不外泄的原因），
// 而 REQ-CAP-001 AC1 要求错误体里出现字段名。
type fieldError struct {
	cause  *apperr.Error
	fields []string
}

func (e fieldError) Error() string { return e.cause.Error() }

func (e fieldError) Unwrap() error { return e.cause }

func invalidField(name, detail string) error {
	return fieldError{
		cause:  apperr.New(apperr.CodeInvalidRequest).WithDetail(detail),
		fields: strings.Split(name, ","),
	}
}

// fieldsOf 取出错误里携带的字段名，没有则返回 nil。
func fieldsOf(err error) []string {
	var typed fieldError
	if errors.As(err, &typed) {
		return typed.fields
	}
	return nil
}
