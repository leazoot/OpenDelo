package httpapi

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/clock"
)

/*
 * 广播器必须存在（回归）。
 *
 * 8787 面原本这样构造端点：`&endpoints{services: ..., logger: ...}` —— 少填了
 * events。少填不会有编译错误，而后果是**第一次审批决定就让整个进程 panic**：
 * 决策端点走完之后要广播一条事件，那时 e.events 是 nil，`Broker.Publish`
 * 在锁上解引用。用户看到的是网关直接消失，账本里也没有那次决定。
 *
 * 由 E2E 的 S5 / S6 / S9 三条用例先撞出来（那三条都要先批准一次），
 * 这里把它收成一个不依赖真实进程的用例。
 */

func TestNewEndpoints_WithoutABroker_StillPublishesWithoutPanicking_Regression(t *testing.T) {
	endpoints := newEndpoints(
		Services{Clock: clock.NewFixed(time.Unix(0, 0).UTC())},
		nil,
		slog.New(slog.DiscardHandler))

	if endpoints.events == nil {
		t.Fatal("端点没有广播器 —— 一次审批决定就会让进程 panic")
	}

	// 真的广播一次。nil 广播器在这一行崩掉。
	endpoints.publish(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/approvals/x/allow-once", nil),
		"passage", map[string]string{"id": "x"})
}

// TestNewEndpoints_WithABroker_UsesThatOne：给了广播器就用给的那个，
// 否则 SSE 订阅者挂在一个没人往里写的广播器上。
func TestNewEndpoints_WithABroker_UsesThatOne(t *testing.T) {
	given := NewBroker(slog.New(slog.DiscardHandler))

	endpoints := newEndpoints(Services{}, given, slog.New(slog.DiscardHandler))

	if endpoints.events != given {
		t.Error("端点没有使用传入的广播器")
	}
}

/*
 * 一条事件必须一次写完（回归）。
 *
 * `writeEvent` 原本分五次 `w.Write` 拼这一帧。`net/http` 的响应体后面是一个
 * 2048 字节的 bufio：超过它的帧因此被切成多次 socket 写。Chromium 与 Firefox
 * 会一直读到读空，**WebKit 每次「有数据了」只读一次、不重新轮询** —— 后半截
 * 要等下一条事件才到得了 Console，而这条流不做重放。
 *
 * 实测（Playwright 1.62 的 WebKit，2026-08-05）：一条 2884 字节的到达事件
 * 先到 2048 字节，剩下的 836 字节要等**下一条事件**才交付。用户看到的是
 * 「缝前来了人，Safari 上什么也不出现」，而且在没有第二条事件的安静时段里
 * 永远不出现。
 *
 * 守的是因不是果：果要一个真的 WebKit 才看得见（`test/e2e` 的兼容性用例），
 * 而因在这里一句话就能钉住 —— 一帧一次写完。
 */
func TestWriteEvent_SendsTheWholeFrameInOneWrite(t *testing.T) {
	body := httptest.NewRecorder()
	recorder := &countingWriter{ResponseWriter: body}

	// 撑到 2048 以上：短于 bufio 的帧就算分几次写也只产生一次 socket 写，
	// 那样的用例会在缺陷仍然存在时通过。
	long := make([]byte, 4096)
	for index := range long {
		long[index] = 'x'
	}
	payload, err := json.Marshal(map[string]string{"resource": string(long)})
	if err != nil {
		t.Fatalf("构造事件失败：%v", err)
	}

	if err = writeEvent(recorder, Event{Type: EventArrival, Data: payload, At: "2026-08-05T00:00:00Z"}); err != nil {
		t.Fatalf("写出事件失败：%v", err)
	}

	if recorder.writes != 1 {
		t.Errorf("一帧写了 %d 次，期望 1 次 —— 分开写的话 WebKit 上后半截要等下一条事件",
			recorder.writes)
	}
	frame := body.Body.String()
	if !strings.HasPrefix(frame, "event: "+EventArrival+"\ndata: ") || !strings.HasSuffix(frame, "\n\n") {
		t.Errorf("帧的格式不对：%.40q…", frame)
	}
}

// countingWriter 记下 Write 被叫了几次。
type countingWriter struct {
	http.ResponseWriter
	writes int
}

func (c *countingWriter) Write(payload []byte) (int, error) {
	c.writes++
	return c.ResponseWriter.Write(payload)
}

// subscriberCount 读一眼此刻登记了几个订阅者。
func (b *Broker) subscriberCount() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return len(b.subscribers)
}

func TestStream_TheSubscriptionExistsBeforeTheClientSeesTheResponse(t *testing.T) {
	/*
	 * 「响应到了」必须已经意味着「我在听了」。
	 *
	 * Console 的做法正是等 /v1/events 有响应就认为自己订阅上了，然后才去触发
	 * 请求。若订阅在写响应头**之后**才建立，这中间广播的事件全部丢失 ——
	 * 而这条流不做重放，丢了就是永远不出现：缝前长不出那张卡片。
	 *
	 * 本机回环上那个窗口是微秒级，因此开发机上一直看不见。2026-08-07 的 CI 上
	 * Linux WebKit 稳定踩中（`compatibility.spec.ts` 的核心流程）。
	 *
	 * 用例检查的是结构而不是时序：响应头一到，订阅数就必须已经是 1。
	 * 反过来写的话这里读到的是 0 —— 不必等，也不必赌。
	 */
	broker := NewBroker(slog.New(slog.DiscardHandler))
	t.Cleanup(broker.Close)

	endpoints := newEndpoints(
		Services{Clock: clock.NewFixed(time.Unix(0, 0).UTC())}, broker,
		slog.New(slog.DiscardHandler),
	)
	server := httptest.NewServer(http.HandlerFunc(endpoints.stream))
	t.Cleanup(server.Close)

	// 跑几轮：一轮碰巧通过说明不了什么，而这条守的正是「不靠运气」。
	for round := 1; round <= 20; round++ {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		response, err := http.DefaultClient.Do(request) //nolint:bodyclose // 本轮末尾关闭
		if err != nil {
			t.Fatalf("第 %d 轮连接失败：%v", round, err)
		}
		if got := broker.subscriberCount(); got != 1 {
			t.Fatalf("第 %d 轮：响应头已经到了，登记的订阅者却有 %d 个 —— "+
				"这中间广播的事件会全部丢失", round, got)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("关闭响应失败：%v", closeErr)
		}
		// 等这一轮注销掉，下一轮才从 0 开始。
		for waited := 0; broker.subscriberCount() != 0 && waited < 200; waited++ {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestStream_EachEventIsFollowedByACommentFrame(t *testing.T) {
	/*
	 * 每条事件后面必须再补一帧。
	 *
	 * 实测（Linux WebKit，Playwright 1.62，2026-08-08）：一次 2897 字节的写入
	 * 只有开头的 123 字节被交给页面，剩下的 2774 字节一直不交，直到这条连接上
	 * 过一会儿又有数据。安静时段里那就是二十秒后的心跳 —— 在那之前 Console
	 * 手里是半条 JSON，解析不出来也不报错，缝前一直写着「无人等待」。
	 *
	 * 守的是因不是果：果要一个真的 WebKit 才看得见（test/e2e 的兼容性用例），
	 * 因在这里一句话钉住 —— 事件之后必须还有一帧。
	 */
	broker := NewBroker(slog.New(slog.DiscardHandler))
	t.Cleanup(broker.Close)

	endpoints := newEndpoints(
		Services{Clock: clock.NewFixed(time.Unix(0, 0).UTC())}, broker,
		slog.New(slog.DiscardHandler),
	)
	server := httptest.NewServer(http.HandlerFunc(endpoints.stream))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("关闭响应失败：%v", closeErr)
		}
	})

	reader := bufio.NewReader(response.Body)
	if got := readFrame(t, reader); got != "retry: 2000\n\n" {
		t.Fatalf("第一帧不是 retry：%q", got)
	}

	payload, err := json.Marshal(map[string]string{"id": "rq-1"})
	if err != nil {
		t.Fatalf("构造事件失败：%v", err)
	}
	broker.Publish(Event{Type: EventArrival, Data: payload, At: "2026-08-08T00:00:00Z"})

	if got := readFrame(t, reader); !strings.HasPrefix(got, "event: "+EventArrival+"\n") {
		t.Fatalf("事件帧的格式不对：%q", got)
	}
	if got := readFrame(t, reader); got != ":\n\n" {
		t.Errorf("事件后面跟的是 %q，期望一个空注释帧 —— "+
			"少了它，Linux WebKit 上这条事件的后半截要等到二十秒后的心跳才到得了 Console", got)
	}
}

// readFrame 读出一整帧（读到空行为止）。
//
// data 行里不会有裸换行（JSON 已转义），因此按行读到空行就是一帧。
func readFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("读取事件流失败：%v", err)
		}
		frame.WriteString(line)
		if strings.HasSuffix(frame.String(), "\n\n") {
			return frame.String()
		}
	}
}
