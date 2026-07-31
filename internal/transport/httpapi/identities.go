package httpapi

import (
	"errors"
	"net/http"
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
type connectBody struct {
	// CredentialReferenceID 指向一份已经登记好的凭据引用。
	// 这里收的是引用而不是明文：明文从不经过 Web API（REQ-CRED-001）。
	CredentialReferenceID string `json:"credential_reference_id"`
	AccountLabel          string `json:"account_label"`
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
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[IdentityView]{Items: items})
}

// connectIdentity 用一份已登记的凭据引用建立一个身份。
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
	environment, err := body.validate()
	if err != nil {
		e.fail(w, r, err)
		return
	}

	reference, err := e.services.Credentials.Reference(
		r.Context(), body.CredentialReferenceID)
	if err != nil {
		e.fail(w, r, err)
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
// 服务名不从请求体里取：它由凭据引用决定。让调用方自己填一个服务名，
// 就能造出「引用指向 GitHub、身份自称 Cloudflare」的一条记录。
func (b connectBody) validate() (matcher.Environment, error) {
	switch {
	case strings.TrimSpace(b.CredentialReferenceID) == "":
		return "", missingFields("credential_reference_id")
	case strings.TrimSpace(b.AccountLabel) == "":
		return "", missingFields("account_label")
	}

	environment := matcher.Environment(b.Environment)
	if environment != matcher.EnvironmentProduction &&
		environment != matcher.EnvironmentNonProduction {
		return "", invalidField("environment",
			"environment 只能是 production 或 non-production")
	}
	return environment, nil
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
