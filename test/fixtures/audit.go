package fixtures

import (
	"time"

	"github.com/Runcoor/opendelo/internal/platform/audit"
)

// EventOption 调整审计事件夹具。
type EventOption func(*audit.Event)

// WithEventID 换掉事件主键。
func WithEventID(id string) EventOption {
	return func(event *audit.Event) { event.ID = id }
}

// WithEventType 换掉事件类型。
func WithEventType(eventType audit.EventType) EventOption {
	return func(event *audit.Event) { event.Type = eventType }
}

// WithEventAgentID 换掉发起者。空串表示当时认不出 Agent。
func WithEventAgentID(agentID string) EventOption {
	return func(event *audit.Event) { event.AgentID = agentID }
}

// WithEventService 换掉服务名。
func WithEventService(service string) EventOption {
	return func(event *audit.Event) { event.Service = service }
}

// WithEventOutcome 换掉执行结果。
func WithEventOutcome(outcome audit.Outcome) EventOption {
	return func(event *audit.Event) { event.Outcome = outcome }
}

// WithEventCreatedAt 换掉发生时刻。
func WithEventCreatedAt(at time.Time) EventOption {
	return func(event *audit.Event) { event.CreatedAt = at }
}

// WithEventResponseStatus 换掉外部服务返回的状态码。0 表示没有发出过请求。
func WithEventResponseStatus(status int) EventOption {
	return func(event *audit.Event) { event.ResponseStatus = status }
}

// WithEventMetadata 换掉元数据文本，用于验证 JSON 约束。
func WithEventMetadata(metadata string) EventOption {
	return func(event *audit.Event) { event.Metadata = metadata }
}

// WithEventUnidentified 把事件构造成「认不出请求者」的样子：
// Agent、设备、工作区、身份、决策链上的引用全部为空。
// Fail Closed 的拒绝就长这样。
func WithEventUnidentified() EventOption {
	return func(event *audit.Event) {
		event.AgentID = ""
		event.DeviceID = ""
		event.WorkspaceID = ""
		event.IdentityID = ""
		event.CredentialProviderID = ""
		event.DecisionID = ""
		event.ApprovalID = ""
		event.LeaseID = ""
		event.LeaseStatus = ""
		event.Verdict = ""
		event.RiskLevel = ""
	}
}

// Event 构造一条合法的审计事件。
//
// 默认是一次自动放行：认得出 Agent、匹配到身份、有决策记录。
// 需要「什么都认不出」的那一类事件时用 WithEventUnidentified。
func Event(options ...EventOption) audit.Event {
	event := audit.Event{
		ID:                   DefaultEventID,
		OperationID:          "01K1OPERATION00000000000000",
		Type:                 audit.EventAutoAllowed,
		AgentID:              DefaultAgentID,
		DeviceID:             DefaultDeviceID,
		WorkspaceID:          DefaultWorkspaceID,
		IdentityID:           DefaultIdentityID,
		CredentialProviderID: DefaultProviderID,
		Service:              DefaultServiceLabel,
		Operation:            "repo.read",
		Resource:             `{"repo":"Runcoor/opendelo"}`,
		ResolvedScope:        `{"service":"github","repo":"Runcoor/opendelo"}`,
		Verdict:              "auto_allow",
		RiskLevel:            "low",
		DecisionID:           DefaultDecisionID,
		Outcome:              audit.OutcomeSucceeded,
		ResponseStatus:       200,
		Duration:             120 * time.Millisecond,
		IsRedacted:           true,
		Metadata:             `{"match_level":"workspace_binding"}`,
		CreatedAt:            Instant,
	}
	for _, apply := range options {
		apply(&event)
	}
	return event
}
