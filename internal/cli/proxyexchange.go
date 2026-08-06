package cli

import (
	"context"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/transport/proxy"
)

/*
 * Exchange 在组装根的接线。
 *
 * 两处窄接口的实现。它们之所以在这里而不在两端，是因为依赖方向不允许：
 * adapter 不得 import core（身份仓储在 core），transport 不得 import adapter。
 * 组装根是唯一同时看得见两边的地方。
 */

// credentialReferences 回答「这个身份用的是哪条凭据引用」。
type credentialReferences struct {
	identities matcher.IdentityRepository
}

var _ adapters.CredentialReferences = (*credentialReferences)(nil)

// ReferenceFor 取出该身份绑定的凭据引用主键。
//
// 身份存在但没有绑定引用时明确拒绝，不返回空串：空串会被当成一条合法引用
// 一路带下去，直到取凭据时才报错，而那时错误信息指向的是引用不存在，
// 与真正的原因（这个身份还没配凭据）差得很远。
func (c *credentialReferences) ReferenceFor(ctx context.Context, identityID string) (string, error) {
	identity, err := c.identities.IdentityByID(ctx, identityID)
	if err != nil {
		return "", err
	}
	if identity.CredentialReferenceID == "" {
		return "", apperr.New(apperr.CodeProviderUnavailable).
			WithDetail("身份 " + identityID + " 还没有绑定凭据引用")
	}
	return identity.CredentialReferenceID, nil
}

// proxyExchange 把 8788 的执行请求转给 adapter 层。
type proxyExchange struct {
	exchange *adapters.Exchange
}

var _ proxy.Exchange = (*proxyExchange)(nil)

// Send 执行一次已经拿到 Lease 的请求。
//
// 转换里没有任何判断。凭据、脱敏、端点白名单全在 adapter 那一侧完成 ——
// 接入面拿到的自始至终只有已经可以交给 Agent 的内容。
func (e *proxyExchange) Send(
	ctx context.Context, grant proxy.Grant, route proxy.Route, body []byte,
) (proxy.Reply, error) {
	reply, err := e.exchange.Send(ctx, adapters.ExchangeRequest{
		Service:     route.Service,
		Operation:   route.Operation,
		IdentityID:  grant.IdentityID,
		Resource:    route.Resource,
		Body:        body,
		OperationID: logging.OperationIDFrom(ctx),
	})
	if err != nil {
		return proxy.Reply{}, err
	}
	return proxy.Reply{
		StatusCode:     reply.StatusCode,
		ContentType:    "application/json",
		Body:           reply.Body,
		UpstreamStatus: reply.UpstreamStatus,
	}, nil
}
