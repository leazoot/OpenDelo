package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * SSE 实时流（REQ-API-002 AC2、假设 A-08）。
 *
 * 四类事件，与 REST 的响应结构一致（同一批 view 函数）：Console 因此不需要
 * 为「推过来的」与「拉回来的」各写一套解析。
 *
 * 这条流**只广播已经发生的事**，不承载任何决定：断线、丢事件、订阅不上，
 * 后果都只是界面暂时不刷新，绝不会让一次请求被放行。Console 重连后
 * 重新拉一次 REST 就能对齐 —— 这也是它不需要事件重放的原因。
 */

// 四类事件（REQ-API-002 AC2）。
const (
	// EventArrival 是一个新的请求停在了缝前。
	EventArrival = "arrival"
	// EventPassage 是一次穿越：放行、拒绝或人工决定的结果。
	EventPassage = "passage"
	// EventLease 是 Lease 的签发、缩短或收回。
	EventLease = "lease"
	// EventGateway 是 Gateway 自身的状态。
	EventGateway = "gateway"
)

const (
	// heartbeatInterval 是没有事件时的心跳间隔。
	//
	// 没有心跳时，中间的任何一层（浏览器、代理、系统休眠）都可能默默掐断一条
	// 看起来空闲的连接，而两边都不会知道。
	heartbeatInterval = 20 * time.Second
	// subscriberBuffer 是每个订阅者的缓冲深度。
	//
	// 慢订阅者会被丢事件而不是把广播端阻塞住：一个卡住的浏览器标签页
	// 不能让整台 Gateway 的决策链路等它。
	subscriberBuffer = 32
	// retryHint 告诉浏览器断线后多久重连（SSE 的 retry 字段，单位毫秒）。
	retryHint = 2000
)

// Event 是推送给 Console 的一条事件。
//
// Data 用 json.RawMessage 装已经序列化好的视图：广播端因此不需要知道
// 每一类事件的具体结构，而结构本身仍由 views.go 那一处决定。
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	// At 是事件发生的时刻，Console 用它给乱序到达的事件排序。
	At string `json:"at"`
}

// Broker 把事件广播给全部订阅者。
//
// 零值不可用，用 NewBroker 构造。
type Broker struct {
	mutex       sync.Mutex
	subscribers map[int]chan Event
	next        int
	closed      bool
	logger      *slog.Logger
}

// NewBroker 构造一个广播器。
func NewBroker(logger *slog.Logger) *Broker {
	return &Broker{subscribers: make(map[int]chan Event), logger: logger}
}

// Publish 把一条事件送给当前全部订阅者。
//
// **不阻塞**：缓冲满的订阅者被跳过并记一条 warn。这条流是通知而不是账本，
// 丢一条事件的后果是界面晚一点刷新，而阻塞的后果是决策链路被一个
// 卡住的浏览器标签页拖住。
func (b *Broker) Publish(event Event) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.closed {
		return
	}
	for id, subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			b.logger.Warn("SSE 订阅者跟不上，丢弃一条事件",
				slog.Int("subscriber", id), slog.String("type", event.Type))
		}
	}
}

// Subscribe 登记一个订阅者，返回它的通道与注销函数。
//
// 调用方**必须**在用完时调用注销函数：不注销的话，每一条事件都要为一个
// 永远收不到的订阅者走一次 select。通道在注销后被关闭。
//
// 缓冲有限且写入不阻塞：读得慢的订阅者会被丢事件（见 Publish）。
// 这条流是通知而不是账本 —— 需要完整历史的地方去查账本。
func (b *Broker) Subscribe() (<-chan Event, func()) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.closed {
		closedChannel := make(chan Event)
		close(closedChannel)
		return closedChannel, func() {}
	}

	id := b.next
	b.next++
	stream := make(chan Event, subscriberBuffer)
	b.subscribers[id] = stream

	return stream, func() {
		b.mutex.Lock()
		defer b.mutex.Unlock()
		if subscriber, present := b.subscribers[id]; present {
			delete(b.subscribers, id)
			close(subscriber)
		}
	}
}

// Close 关闭全部订阅并拒绝后续订阅，供进程退出时调用。
func (b *Broker) Close() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for id, subscriber := range b.subscribers {
		delete(b.subscribers, id)
		close(subscriber)
	}
}

// Subscribers 返回当前订阅者数量，供用例断言注销确实发生了。
func (b *Broker) Subscribers() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return len(b.subscribers)
}

// publish 把一个视图序列化后广播出去。
//
// 序列化失败只记日志：这条流是通知而不是账本，广播不出去的后果是界面
// 晚一点刷新，而让它把请求带失败就是拿一次真实的授权去换一次界面更新。
func (e *endpoints) publish(r *http.Request, eventType string, view any) {
	payload, err := json.Marshal(view)
	if err != nil {
		e.logger.WarnContext(r.Context(), "事件无法编码，未广播",
			slog.String("type", eventType),
			slog.String("operation_id", logging.OperationIDFrom(r.Context())))
		return
	}

	e.events.Publish(Event{
		Type: eventType,
		Data: payload,
		At:   formatTime(e.services.Clock.Now()),
	})
}

// stream 是 GET /v1/events。
//
// Agent 拿不到这条流：它会把「别人正在做什么」逐条播给发起方
// （REQ-DECIDE-004 的边界）。
func (e *endpoints) stream(w http.ResponseWriter, r *http.Request) {
	if err := e.refuseAgent(w, r, "事件流"); err != nil {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// 没有 Flusher 的写入端会把事件攒在缓冲里，那不是一条实时流。
		e.fail(w, r, apperr.New(apperr.CodeInternal).
			WithDetail("当前的响应写入端不支持流式推送"))
		return
	}

	header := w.Header()
	header.Set(headerContentType, "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	// 关掉中间层的缓冲。本机回环上没有反向代理，但这条头是零成本的保险。
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, unsubscribe := e.events.Subscribe()
	defer unsubscribe()

	// retry 让浏览器在断线后自动重连（REQ-API-002 的「断线可重连」）：
	// 服务端不做事件重放，Console 重连后重新拉一次 REST 即可对齐。
	if _, err := w.Write([]byte("retry: " + itoa(retryHint) + "\n\n")); err != nil {
		// 连第一行都写不出去，这条连接已经没了，直接收工。
		return
	}
	flusher.Flush()

	e.pump(r.Context(), w, flusher, events)
}

// pump 把事件与心跳写出去，直到连接结束。
func (e *endpoints) pump(
	ctx context.Context, w http.ResponseWriter, flusher http.Flusher, events <-chan Event,
) {
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeEvent(w, event); err != nil {
				e.logger.WarnContext(ctx, "写出 SSE 事件失败",
					slog.String("type", event.Type),
					slog.String("operation_id", logging.OperationIDFrom(ctx)))
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent 按 SSE 的行格式写出一条事件。
//
// data 是一行 JSON：视图里的字符串已经过 JSON 转义，不会含裸换行，
// 因此不需要按行拆分（拆分会让 Console 侧多一步拼接）。
func writeEvent(w http.ResponseWriter, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}

	if _, err = w.Write([]byte("event: " + event.Type + "\n")); err != nil {
		return err
	}
	if _, err = w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err = w.Write(payload); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}

// itoa 避免为一个常量引入 strconv。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
