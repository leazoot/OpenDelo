package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/core/approval"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/transport/httpapi"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 到达通知播出去的形状（REQ-API-002 AC2）。
 *
 * 「与 REST 的响应结构一致」不是格式上的洁癖：Console 的缝前列表按请求主键把
 * 推来的与拉回来的合成同一份，而**能不能在缝前做决定，取决于那一行有没有
 * approval 主键与 available_actions**。停在缝前的请求若只播一份请求视图，
 * 卡片会出现、却点不动也按不动 —— 用户看得见一个决定不了的东西（回归）。
 */

// listen 订阅事件流并返回下一条事件的解码结果。
func nextArrival(t *testing.T, broker *httpapi.Broker) func() map[string]any {
	t.Helper()

	events, unsubscribe := broker.Subscribe()
	t.Cleanup(unsubscribe)

	return func() map[string]any {
		select {
		case event := <-events:
			var decoded map[string]any
			if err := json.Unmarshal(event.Data, &decoded); err != nil {
				t.Fatalf("事件体不是合法 JSON：%v", err)
			}
			if event.Type != httpapi.EventArrival {
				t.Fatalf("事件类型为 %q，期望 arrival", event.Type)
			}
			return decoded
		case <-time.After(3 * time.Second):
			t.Fatal("三秒内没有等到事件")
			return nil
		}
	}
}

func newAnnouncer(t *testing.T) (*httpapi.Announcer, func() map[string]any) {
	t.Helper()

	quiet := slog.New(slog.NewJSONHandler(io.Discard, nil))
	broker := httpapi.NewBroker(quiet)
	t.Cleanup(broker.Close)

	announcer, err := httpapi.NewAnnouncer(httpapi.Announcement{
		Events:       broker,
		Capabilities: fixtures.NewGateway(t).Services.Capabilities,
		Clock:        clock.NewFixed(fixtures.Instant),
		Logger:       quiet,
	})
	if err != nil {
		t.Fatalf("构造到达通知失败：%v", err)
	}
	return announcer, nextArrival(t, broker)
}

func TestAnnounce_AWaitingRequestCarriesItsApprovalAndActions_Regression(t *testing.T) {
	announcer, next := newAnnouncer(t)

	announcer.Announce(t.Context(), pipeline.Result{
		Request:  fixtures.Request(),
		Decision: fixtures.Decision(fixtures.WithDecisionVerdict(decision.VerdictRequireApproval)),
		Approval: &approval.Approval{
			ID: "01K1APPROVAL0000000000000A", DecisionID: fixtures.DefaultDecisionID,
			Status: approval.StatusPending, ExpiresAt: fixtures.Instant.Add(time.Hour),
			CreatedAt: fixtures.Instant,
		},
	})

	event := next()
	if event["id"] != "01K1APPROVAL0000000000000A" {
		t.Errorf("事件里的 id 是 %v，期望审批项主键 —— 缝上那一行将不知道要决定哪一条", event["id"])
	}
	actions, ok := event["available_actions"].([]any)
	if !ok || len(actions) == 0 {
		t.Fatalf("事件里没有 available_actions（%v）—— 缝前的那张卡片按不动", event["available_actions"])
	}
	if _, present := event["request"]; !present {
		t.Error("事件里没有 request，缝上画不出这次请求是什么")
	}
}

// TestAnnounce_AnAutoAllowedRequestCarriesTheRequestItself：自动放行的那些没有
// 审批项，播的就是请求本身。缝上仍然要出现这一行 —— 「有东西穿过去了」
// 正是产品最想让人看见的那一半。
func TestAnnounce_AnAutoAllowedRequestCarriesTheRequestItself(t *testing.T) {
	announcer, next := newAnnouncer(t)

	announcer.Announce(t.Context(), pipeline.Result{
		Request:  fixtures.Request(),
		Decision: fixtures.Decision(fixtures.WithDecisionVerdict(decision.VerdictAutoAllow)),
	})

	event := next()
	if event["id"] != fixtures.DefaultRequestID {
		t.Errorf("事件里的 id 是 %v，期望能力请求主键", event["id"])
	}
	if _, present := event["operation_id"]; !present {
		t.Error("事件里没有 operation_id，这一次穿越在账本上串不起来")
	}
}

func TestNewAnnouncer_MissingAnyDependency_IsRefused(t *testing.T) {
	quiet := slog.New(slog.NewJSONHandler(io.Discard, nil))
	complete := httpapi.Announcement{
		Events:       httpapi.NewBroker(quiet),
		Capabilities: fixtures.NewGateway(t).Services.Capabilities,
		Clock:        clock.NewFixed(fixtures.Instant),
		Logger:       quiet,
	}
	if _, err := httpapi.NewAnnouncer(complete); err != nil {
		t.Fatalf("依赖齐全却构造失败：%v", err)
	}

	blanked := map[string]func(*httpapi.Announcement){
		"事件流":  func(a *httpapi.Announcement) { a.Events = nil },
		"能力清单": func(a *httpapi.Announcement) { a.Capabilities = nil },
		"时钟":   func(a *httpapi.Announcement) { a.Clock = nil },
		"日志":   func(a *httpapi.Announcement) { a.Logger = nil },
	}
	for name, blank := range blanked {
		t.Run(name, func(t *testing.T) {
			incomplete := complete
			blank(&incomplete)
			if _, err := httpapi.NewAnnouncer(incomplete); err == nil {
				t.Errorf("缺少%s却构造成功了", name)
			}
		})
	}
}
