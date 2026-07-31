package registry_test

import (
	"context"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 执行前查勘的用例（REQ-APPROVAL-001 AC4）。
 *
 * 这条路比执行更危险：它在**人还没同意之前**就带着凭据出站。因此用例守的是
 * 三件事 —— 只走只读操作、拿不到旧值不算失败之外的任何东西、凭据用完清零。
 *
 * 「只走只读」这一条不能只靠 Adapter 自己记得：Adapter 报出它要走哪个操作，
 * 由本包按能力声明核对。用例因此故意让 Adapter 报一个写操作，断言那时
 * **一个字节都没发出去**。
 */

const previewOperationID = "operation_preview_test"

// previewAdapter 是一个可以摆布的 Previewer。
//
// 不用真实 Adapter：这里要证明的是**注册表这一侧的核对**，因此必须能让
// Adapter 报出一个不该报的操作 —— 真实 Adapter 报不出来。
type previewAdapter struct {
	source  string
	calls   int
	changes []registry.ResourceChange
}

func (p *previewAdapter) Service() string     { return "previewsvc" }
func (p *previewAdapter) Kind() registry.Kind { return registry.KindGenericHTTP }

func (p *previewAdapter) Capabilities() []registry.Capability {
	return []registry.Capability{
		{
			Operation: "read_thing", InputSchema: "{}",
			MinimumScope: registry.MinimumScope{ResourceKeys: []string{"id"}},
			RiskLabel:    registry.RiskLabelLow,
			Method:       "GET", Path: "/things/{id}",
			RedactionRules: []string{}, ResponseFields: []string{"id", "value"},
			Rollback: registry.RollbackManual, Idempotency: registry.Idempotent,
		},
		{
			Operation: "update_thing", InputSchema: "{}",
			MinimumScope: registry.MinimumScope{ResourceKeys: []string{"id"}},
			RiskLabel:    registry.RiskLabelMedium,
			Method:       "PATCH", Path: "/things/{id}",
			RedactionRules: []string{}, ResponseFields: []string{"id", "value"},
			Rollback: registry.RollbackManual, Idempotency: registry.Idempotent,
		},
	}
}

func (p *previewAdapter) PreviewSource(string) string { return p.source }

func (p *previewAdapter) PreviewCapability(
	_ context.Context, input registry.PreviewInput,
) (registry.PreviewOutput, error) {
	p.calls++
	// 查勘拿到的确实是明文：拿不到的话这次查询在真实服务上会 401，
	// 而那种失败在用例里与「压根没带凭据」长得一样。
	if string(input.Credential.Reveal()) != sentinel.SentinelToken {
		return registry.PreviewOutput{}, apperr.New(apperr.CodeProviderUnavailable).
			WithDetail("查勘没有拿到凭据明文")
	}
	return registry.PreviewOutput{Changes: p.changes}, nil
}

// silentAdapter 只声明能力，不实现查勘。
//
// 不能嵌入 previewAdapter：方法会被提升上来，于是它照样满足 Previewer，
// 这个用例就再也证明不了「不实现查勘的 Adapter 会被明确拒绝」。
type silentAdapter struct{ capabilities []registry.Capability }

func (s silentAdapter) Service() string                     { return "silentsvc" }
func (s silentAdapter) Kind() registry.Kind                 { return registry.KindGenericHTTP }
func (s silentAdapter) Capabilities() []registry.Capability { return s.capabilities }

func newPreviewExchange(
	t *testing.T, adapter registry.Adapter, credentials registry.Credentials,
) *registry.Exchange {
	t.Helper()

	all, err := registry.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}
	exchange, err := registry.NewExchange(all, credentials,
		stubReferences{referenceID: "reference_1"})
	if err != nil {
		t.Fatalf("构造 Exchange 失败：%v", err)
	}
	return exchange
}

func updateThing(service string) registry.ExchangeRequest {
	return registry.ExchangeRequest{
		Service: service, Operation: "update_thing", IdentityID: "identity_1",
		Resource: map[string]string{"id": "thing_1"},
		Body:     []byte(`{"value":"after"}`), OperationID: previewOperationID,
	}
}

func TestExchangePreview_ReadOnlySource_ReturnsTheOldValueAndZeroesTheCredential(t *testing.T) {
	adapter := &previewAdapter{
		source:  "read_thing",
		changes: []registry.ResourceChange{{Resource: "thing_1", Field: "value", Before: "before", After: "after"}},
	}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	preview, err := exchange.Preview(t.Context(), updateThing("previewsvc"))
	if err != nil {
		t.Fatalf("查勘失败：%v", err)
	}

	if len(preview.Changes) != 1 || preview.Changes[0].Before != "before" {
		t.Errorf("查勘结果为 %+v，期望带上旧值", preview.Changes)
	}
	if credentials.fetch != 1 {
		t.Errorf("取了 %d 次凭据，期望恰好 1 次", credentials.fetch)
	}
	// 与执行那条路同一条规矩：用完即清零。
	if len(credentials.value.Reveal()) != 0 {
		t.Error("查勘结束后凭据没有被清零")
	}
}

func TestExchangePreview_SourceThatWrites_IsRefusedBeforeAnythingIsSent(t *testing.T) {
	// 四道闸的第一道。查勘发生在人同意之前，它能改变外部状态就等于
	// 绕过了审批 —— 因此这里不只要求「返回错误」，还要求凭据一次都没被取出来。
	adapter := &previewAdapter{source: "update_thing"}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	_, err := exchange.Preview(t.Context(), updateThing("previewsvc"))
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if adapter.calls != 0 {
		t.Errorf("Adapter 的查勘被调用了 %d 次，期望 0 次", adapter.calls)
	}
	if credentials.fetch != 0 {
		t.Errorf("取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
}

func TestExchangePreview_UndeclaredSource_IsRefusedBeforeAnythingIsSent(t *testing.T) {
	// 报一个注册表里根本没有的操作名与报一个写操作同样危险：它绕过的是
	// 端点白名单本身。
	adapter := &previewAdapter{source: "read_everything"}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	_, err := exchange.Preview(t.Context(), updateThing("previewsvc"))
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if adapter.calls != 0 || credentials.fetch != 0 {
		t.Errorf("查勘调用了 %d 次、取凭据 %d 次，期望都是 0", adapter.calls, credentials.fetch)
	}
}

func TestExchangePreview_NoSource_TouchesNoCredential(t *testing.T) {
	// 「这个操作没有旧值可查」不是失败：新建一条记录时，改之前那条记录还不存在。
	adapter := &previewAdapter{source: ""}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	preview, err := exchange.Preview(t.Context(), updateThing("previewsvc"))
	if err != nil {
		t.Fatalf("没有旧值可查被当成了失败：%v", err)
	}
	if len(preview.Changes) != 0 {
		t.Errorf("查勘结果为 %+v，期望为空", preview.Changes)
	}
	if credentials.fetch != 0 {
		t.Errorf("取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
}

func TestExchangePreview_UndeclaredOperation_IsRefusedBeforeTheCredentialIsFetched(t *testing.T) {
	adapter := &previewAdapter{source: "read_thing"}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	request := updateThing("previewsvc")
	request.Operation = "delete_everything"

	if _, err := exchange.Preview(t.Context(), request); !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
	if credentials.fetch != 0 {
		t.Errorf("取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
}

func TestExchangePreview_AdapterWithoutPreview_ReportsNotImplemented(t *testing.T) {
	// 不实现查勘的 Adapter 只是没有旧值可展示，卷宗照实说「尚未查询」。
	// 它与「查勘失败了」在类型上分得开，因此上层能分别记日志。
	adapter := silentAdapter{capabilities: (&previewAdapter{}).Capabilities()}
	credentials := sentinelCredential(t)
	exchange := newPreviewExchange(t, adapter, credentials)

	_, err := exchange.Preview(t.Context(), updateThing("silentsvc"))
	if !apperr.Is(err, apperr.CodeNotImplemented) {
		t.Fatalf("错误码为 %s，期望 not_implemented（%v）", apperr.CodeOf(err), err)
	}
	if credentials.fetch != 0 {
		t.Errorf("取了 %d 次凭据，期望 0 次", credentials.fetch)
	}
}
