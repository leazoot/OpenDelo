package httpapi

import (
	"context"
	"log/slog"

	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
)

/*
 * 决策结果 → 已打开的 Console（REQ-API-002 的 arrival 事件）。
 *
 * 广播的落点在决策路径上（`internal/orchestration`），因为三个接入面共用的只有
 * 那一条。这里提供的是**它怎么变成一条事件**：与 REST 同一批 view 函数，
 * Console 因此不必为「推过来的」与「拉回来的」各写一套解析。
 *
 * 这条通知不影响任何一次授权：编码不出来、没有订阅者、订阅者的缓冲满了，
 * 后果都只是界面晚一点刷新。
 */

// Announcement 是 Announcer 的依赖，全部必填。
type Announcement struct {
	// Events 是要广播到的那条流。
	Events *Broker
	// Capabilities 用来算出「仍然关闭的权限」，与 REST 的那一份同源。
	Capabilities Capabilities
	Clock        clock.Clock
	Logger       *slog.Logger
}

// Announcer 把决策结果播到事件流上。实现 orchestration 的到达通知端口。
type Announcer struct {
	events       *Broker
	capabilities Capabilities
	clock        clock.Clock
	logger       *slog.Logger
}

// NewAnnouncer 校验依赖并构造。
//
// 缺任何一项都拒绝构造：一个不会广播的通知器不会报错，只会让缝前一直是静的，
// 而那件事在任何一次调用的返回值里都看不出来。
func NewAnnouncer(built Announcement) (*Announcer, error) {
	switch {
	case built.Events == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("到达通知缺少事件流")
	case built.Capabilities == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("到达通知缺少能力清单")
	case built.Clock == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("到达通知缺少时钟")
	case built.Logger == nil:
		return nil, apperr.New(apperr.CodeInternal).WithDetail("到达通知缺少日志")
	}
	return &Announcer{
		events: built.Events, capabilities: built.Capabilities,
		clock: built.Clock, logger: built.Logger,
	}, nil
}

// Announce 把一次决策的结果播出去。
//
// **停在缝前的那些播审批项，其余播请求本身。** 形状与 `GET /v1/approvals`
// 和 `POST /v1/capability-requests` 的响应一一对应，Console 因此不必为
// 「推过来的」与「拉回来的」各写一套解析。
//
// 这个分岔不是格式偏好：能不能在缝前做决定，取决于那一行有没有 approval 主键
// 与 available_actions。只播请求视图的话，卡片会出现、却点不动也按不动 ——
// 用户看得见一个决定不了的东西。
func (a *Announcer) Announce(ctx context.Context, result pipeline.Result) {
	withheld := withheldOperations(a.capabilities, result.Request.Service, result.Request.Operation)
	request := requestView(result.Request, decidedView(result), withheld)

	var view any = request
	if result.Approval != nil {
		view = approvalView(*result.Approval, availableActions(result.Decision),
			&request, decidedView(result))
	}
	publishView(ctx, a.events, a.clock, a.logger, EventArrival, view)
}
