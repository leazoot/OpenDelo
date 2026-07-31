package audit

import (
	"context"
	"slices"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

// eventTypes 是全部合法的事件类型，顺序与 EventType 的常量声明一致。
// 新增类型必须同时出现在这里、EventType 的常量里、数据库 CHECK 里
// 与前端过滤器里（REQ-AUDIT-002 AC2），四处缺一即失败。
var eventTypes = []EventType{
	EventAutoAllowed, EventUserAllowed, EventDenied,
	EventLeaseCreated, EventLeaseExpired, EventLeaseRevoked,
	EventAdapterExecuted, EventError, EventIdentityMatched, EventRiskChanged,
	EventScopeInjectionIgnored, EventSecretRequestBlocked, EventStrongAuthLocked,
	EventTrustCleared, EventPruned, EventIdentityMismatch, EventAgentTrusted,
}

// EventTypes 返回全部事件类型的副本，供测试与前端定义逐条核对。
func EventTypes() []EventType {
	return slices.Clone(eventTypes)
}

// Recorder 是账本唯一的写入入口。
//
// 它承担三件事，缺一条这一层就形同虚设：
//  1. 事件类型必须在封闭枚举内；
//  2. 三个 JSON 字段先脱敏再落库，metadata 另外要求是扁平键值明细；
//  3. ID 与发生时刻由本层生成，调用方无法伪造。
//
// Record 返回的错误必须被调用方向上传递：审计写入是执行的前置条件，
// 写不进去请求就失败（ADR-004）。这一层不吞错、不异步、不重试。
type Recorder struct {
	repository Repository
	clock      clock.Clock
	ids        *ulid.Generator
}

// NewRecorder 组装写入器。三个依赖都必须给出：
// 缺时钟或 ID 生成器意味着这条记录无法被定位，那样的记录不该被写下。
func NewRecorder(repository Repository, source clock.Clock, ids *ulid.Generator) (*Recorder, error) {
	if repository == nil || source == nil || ids == nil {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("审计写入器需要仓储、时钟与 ID 生成器三者齐全")
	}
	return &Recorder{repository: repository, clock: source, ids: ids}, nil
}

// Record 写入一条审计事件并返回落库后的结果。
//
// 调用方给出的 ID 与 CreatedAt 会被忽略：时刻由网关的时钟决定，
// 否则一次伪造的时间戳就能让账本上的先后关系失真。
func (r *Recorder) Record(ctx context.Context, event Event) (Event, error) {
	if !slices.Contains(eventTypes, event.Type) {
		return Event{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("未登记的审计事件类型 " + string(event.Type))
	}
	if event.OperationID == "" {
		return Event{}, apperr.New(apperr.CodeInvalidRequest).
			WithDetail("审计事件必须带 operation_id，否则这条记录无从追溯")
	}

	prepared, err := r.prepare(event)
	if err != nil {
		return Event{}, err
	}

	written, err := r.repository.Append(ctx, prepared)
	if err != nil {
		// 不包装成别的码，也不吞掉：调用方要凭这个错误让整个请求失败。
		return Event{}, err
	}
	return written, nil
}

func (r *Recorder) prepare(event Event) (Event, error) {
	id, err := r.ids.NewID()
	if err != nil {
		return Event{}, apperr.Wrap(apperr.CodeInternal, err).
			WithDetail("生成审计事件 ID 失败")
	}

	resource, err := redactJSONObject("resource", event.Resource)
	if err != nil {
		return Event{}, err
	}
	resolvedScope, err := redactJSONObject("resolved_scope", event.ResolvedScope)
	if err != nil {
		return Event{}, err
	}
	metadata, err := redactMetadata(event.Metadata)
	if err != nil {
		return Event{}, err
	}

	prepared := event
	prepared.ID = id
	prepared.CreatedAt = r.clock.Now()
	prepared.Resource = resource
	prepared.ResolvedScope = resolvedScope
	prepared.Metadata = metadata
	return prepared, nil
}
