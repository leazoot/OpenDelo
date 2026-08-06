package httpapi_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * SSE 实时流（REQ-API-002 AC2、假设 A-08）。
 *
 * 用真实的 httptest.Server：`httptest.ResponseRecorder` 不是 Flusher，
 * 拿它测流式推送等于测了一个不存在的路径。
 */

// live 起一台真实监听的服务，返回它的地址、背后的 backend 与广播器。
func live(t *testing.T, caller httpapi.Caller) (string, backend, *httpapi.Broker) {
	t.Helper()

	gateway := newBackend(t)
	// 用夹具已经装好的那一个，不另起一个：arrival 由决策路径上的到达通知播出，
	// 它绑的就是这一个。换成新的之后订阅者挂在一条没人往里写的流上。
	broker := gateway.Services.Events

	all := newAPIForBackend(t, gateway, caller)
	server := httptest.NewServer(all.handler)
	t.Cleanup(server.Close)
	return server.URL, all.backend, broker
}

// listener 是一条已经连上的 SSE 流。
type listener struct {
	reader   *bufio.Reader
	response *http.Response
}

func listen(t *testing.T, address string) *listener {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address+"/v1/events", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	response, err := http.DefaultClient.Do(request) //nolint:bodyclose // 由 Cleanup 关闭
	if err != nil {
		t.Fatalf("连接事件流失败：%v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	if response.StatusCode != http.StatusOK {
		t.Fatalf("事件流返回 %d", response.StatusCode)
	}
	if kind := response.Header.Get("Content-Type"); !strings.HasPrefix(kind, "text/event-stream") {
		t.Fatalf("Content-Type 为 %q", kind)
	}
	return &listener{reader: bufio.NewReader(response.Body), response: response}
}

// next 读到下一条事件。超时视为「没等到」，用例据此失败而不是挂住。
func (l *listener) next(t *testing.T) (string, map[string]any) {
	t.Helper()

	type parsed struct {
		name string
		data map[string]any
	}
	found := make(chan parsed, 1)
	failed := make(chan error, 1)

	go func() {
		var name string
		for {
			line, err := l.reader.ReadString('\n')
			if err != nil {
				failed <- err
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var envelope struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
					At   string          `json:"at"`
				}
				if err = json.Unmarshal(
					[]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
					failed <- err
					return
				}
				body := map[string]any{}
				if err = json.Unmarshal(envelope.Data, &body); err != nil {
					failed <- err
					return
				}
				if envelope.Type != name {
					failed <- io.ErrUnexpectedEOF
					return
				}
				if envelope.At == "" {
					failed <- io.ErrUnexpectedEOF
					return
				}
				found <- parsed{name: name, data: body}
				return
			}
		}
	}()

	select {
	case result := <-found:
		return result.name, result.data
	case err := <-failed:
		t.Fatalf("读取事件失败：%v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("三秒内没有等到事件")
	}
	return "", nil
}

func TestStream_AnnouncesItsRetryHintSoBrowsersReconnect(t *testing.T) {
	// REQ-API-002：断线可重连。服务端不做事件重放，Console 重连后
	// 重新拉一次 REST 对齐，因此这里只需要把重连间隔告诉浏览器。
	address, _, _ := live(t, httpapi.Caller{})
	stream := listen(t, address)

	line, err := stream.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("读取首行失败：%v", err)
	}
	if !strings.HasPrefix(line, "retry: ") {
		t.Errorf("首行为 %q，期望 retry", line)
	}
	if cache := stream.response.Header.Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control 为 %q", cache)
	}
}

func TestStream_ArrivalCarriesTheSameShapeAsTheRESTResponse(t *testing.T) {
	// REQ-API-002 AC2：事件体与 REST 结构一致 —— 两边走同一批 view 函数。
	address, _, _ := live(t, httpapi.Caller{})
	stream := listen(t, address)
	drainRetry(t, stream)

	submitted := post(t, address+"/v1/capability-requests", submitBody)
	var view httpapi.CapabilityRequestView
	if err := json.Unmarshal(submitted, &view); err != nil {
		t.Fatalf("提交响应不是合法 JSON：%v", err)
	}

	name, data := stream.next(t)
	if name != httpapi.EventArrival {
		t.Fatalf("事件类型为 %q，期望 arrival", name)
	}
	if data["id"] != view.ID {
		t.Errorf("事件里的 id 是 %v，REST 返回的是 %s", data["id"], view.ID)
	}
	for _, field := range []string{
		"operation_id", "agent_id", "workspace_id", "service", "operation",
		"resource", "desired_change", "reason", "status", "created_at",
	} {
		if _, present := data[field]; !present {
			t.Errorf("事件体缺少 REST 响应里有的字段 %q", field)
		}
	}
}

func TestStream_ReachesEverySubscriberAndUnsubscribesOnDisconnect(t *testing.T) {
	address, _, _ := live(t, httpapi.Caller{})
	first := listen(t, address)
	second := listen(t, address)
	drainRetry(t, first)
	drainRetry(t, second)

	post(t, address+"/v1/capability-requests", submitBody)

	for index, stream := range []*listener{first, second} {
		if name, _ := stream.next(t); name != httpapi.EventArrival {
			t.Errorf("第 %d 个订阅者收到的是 %q", index+1, name)
		}
	}

	// 断开之后广播不能再往那条已经关掉的连接上写。
	if err := second.response.Body.Close(); err != nil {
		t.Fatalf("关闭连接失败：%v", err)
	}
	post(t, address+"/v1/capability-requests", submitBody)
	if name, _ := first.next(t); name != httpapi.EventArrival {
		t.Errorf("断开一个订阅者之后，另一个收到的是 %q", name)
	}
}

func TestStream_PassageAndLeaseAreBothAnnounced(t *testing.T) {
	// 四类事件里的 passage 与 lease：一次人工放行同时产生这两条。
	address, all, _ := live(t, httpapi.Caller{})
	item := waitingOn(t, all)

	stream := listen(t, address)
	drainRetry(t, stream)
	post(t, address+"/v1/approvals/"+item+"/allow-once", "")

	seen := map[string]bool{}
	for range 2 {
		name, _ := stream.next(t)
		seen[name] = true
	}
	if !seen[httpapi.EventPassage] || !seen[httpapi.EventLease] {
		t.Errorf("收到的事件为 %v，期望同时有 passage 与 lease", seen)
	}
}

func TestStream_ReplayedSettlementIsNotAnnouncedTwice(t *testing.T) {
	// 重放没有产生新的后果，再播一次会让 Gate 页面看起来发生了两次决定。
	address, all, _ := live(t, httpapi.Caller{})
	item := waitingOn(t, all)

	stream := listen(t, address)
	drainRetry(t, stream)

	post(t, address+"/v1/approvals/"+item+"/allow-once", "")
	post(t, address+"/v1/approvals/"+item+"/allow-once", "")

	// 首次那一次产生 passage + lease 两条；重放不该再产生第三条。
	// 用一次提交作为「后面没有别的事件了」的分界：如果重放播了，
	// 第三条读到的会是 passage 或 lease 而不是 arrival。
	post(t, address+"/v1/capability-requests", submitBody)

	seen := make([]string, 0, 3)
	for range 3 {
		name, _ := stream.next(t)
		seen = append(seen, name)
	}
	if seen[2] != httpapi.EventArrival {
		t.Errorf("第三条事件是 %q，期望 arrival —— 重放多播了一条：%v", seen[2], seen)
	}
}

func TestStream_AgentIsRefused(t *testing.T) {
	// 这条流会把「别人正在做什么」逐条播给发起方。
	address, _, _ := live(t, httpapi.Caller{AgentID: fixtures.DefaultAgentID})

	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, address+"/v1/events", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("状态码为 %d，期望 403", response.StatusCode)
	}
}

func TestBroker_ASubscriberThatNeverReadsIsDroppedNotWaitedFor(t *testing.T) {
	// 一个卡住的浏览器标签页不能让决策链路等它：缓冲满了就丢事件。
	// 订阅者是真的登记进去的，且一个字节都不读 —— 空广播器上发一万条
	// 什么都证明不了，那条路径根本没有订阅者。
	broker := httpapi.NewBroker(discardLogger())
	stream, unsubscribe := broker.Subscribe()
	defer unsubscribe()

	if broker.Subscribers() != 1 {
		t.Fatalf("订阅者数量为 %d，期望 1", broker.Subscribers())
	}

	const published = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range published {
			broker.Publish(httpapi.Event{Type: httpapi.EventGateway, Data: []byte(`{}`)})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("订阅者一条都不读时，广播被阻塞住了")
	}

	// 丢事件而不是攒着：收到的必须远少于发出的，否则「不阻塞」是靠
	// 一个无界队列换来的，那只是把问题从卡住变成了吃内存。
	delivered := 0
	for {
		select {
		case <-stream:
			delivered++
			continue
		default:
		}
		break
	}
	if delivered >= published {
		t.Errorf("收到了 %d 条而只发出了 %d 条，缓冲是无界的", delivered, published)
	}
	if delivered == 0 {
		t.Error("一条都没收到，订阅者根本没被登记进去")
	}
}

func TestBroker_DisconnectedSubscriberIsUnregistered(t *testing.T) {
	// 不注销的话，每刷新一次页面就在广播器里留下一个永远收不到的订阅者，
	// 而每一条事件都要为它们各走一次 select。
	address, _, broker := live(t, httpapi.Caller{})
	stream := listen(t, address)
	drainRetry(t, stream)
	waitForSubscribers(t, broker, 1)

	if err := stream.response.Body.Close(); err != nil {
		t.Fatalf("关闭连接失败：%v", err)
	}
	// 断开后要发一条事件，处理器才会从 pump 里醒过来走到注销。
	post(t, address+"/v1/capability-requests", submitBody)
	waitForSubscribers(t, broker, 0)
}

func TestBroker_AfterCloseNothingIsDelivered(t *testing.T) {
	broker := httpapi.NewBroker(discardLogger())
	broker.Close()
	// 关闭之后再发一条不该 panic，也不该有订阅者。
	broker.Publish(httpapi.Event{Type: httpapi.EventGateway, Data: []byte(`{}`)})
	if broker.Subscribers() != 0 {
		t.Errorf("关闭之后还有 %d 个订阅者", broker.Subscribers())
	}
	// 关两次也不该 panic。
	broker.Close()
}

// waitForSubscribers 等订阅者数量达到期望值。
//
// 订阅与注销都发生在处理器的 goroutine 里，直接断言会撞上竞态；
// 这里轮询而不是 sleep 一个固定时长。
func waitForSubscribers(t *testing.T, broker *httpapi.Broker, expected int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if broker.Subscribers() == expected {
			return
		}
	}
	t.Fatalf("订阅者数量为 %d，期望 %d", broker.Subscribers(), expected)
}

// ——— 辅助 ———

func drainRetry(t *testing.T, stream *listener) {
	t.Helper()

	for {
		line, err := stream.reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if strings.TrimSpace(line) == "" {
			return
		}
	}
}

func post(t *testing.T, target, body string) []byte {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	if response.StatusCode >= 300 {
		t.Fatalf("%s 返回 %d：%s", target, response.StatusCode, payload)
	}
	return payload
}
