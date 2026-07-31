package gateway_test

import (
	"sync"
	"testing"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/gateway"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 网关可用性的用例（REQ-GATEWAY-003、成功标准 S10）。
 */

func TestAvailability_ZeroValue_RefusesEverything(t *testing.T) {
	// 忘了调用 Serve 的装配路径必须拒绝一切，而不是在没准备好的时候放行。
	var availability gateway.Availability

	if availability.Serving() {
		t.Error("零值就处于服务状态")
	}
	if err := availability.Check(); !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Errorf("零值的 Check 返回 %v，期望 gateway_unavailable", err)
	}
}

func TestAvailability_Serving_LetsRequestsThrough(t *testing.T) {
	// 反向对照：没有这条，上面那条可以靠「Check 永远报错」通过。
	availability := gateway.New()
	if !availability.Serve() {
		t.Fatal("全新的网关拒绝进入服务状态")
	}

	if !availability.Serving() {
		t.Error("Serve 之后仍然不在服务状态")
	}
	if err := availability.Check(); err != nil {
		t.Errorf("服务中的 Check 返回 %v，期望放行", err)
	}
	if blocker, blocked := availability.Blocker(); blocked {
		t.Errorf("服务中却给出了阻断 %q", blocker)
	}
}

func TestAvailability_Stopped_RefusesAndRecordsTheBlocker(t *testing.T) {
	// REQ-GATEWAY-003 AC1：停止后请求失败并收到 gateway_unavailable。
	availability := gateway.New()
	availability.Serve()
	availability.Stop()

	if availability.Serving() {
		t.Error("Stop 之后仍然在服务状态")
	}
	if err := availability.Check(); !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Errorf("停止后的 Check 返回 %v，期望 gateway_unavailable", err)
	}

	blocker, blocked := availability.Blocker()
	if !blocked {
		t.Fatal("停止后没有给出阻断，账本上就查不到这次拒绝的起因")
	}
	if blocker != decision.BlockerGatewayOffline {
		t.Errorf("阻断为 %q，期望 %q", blocker, decision.BlockerGatewayOffline)
	}
}

func TestAvailability_StopIsOneWay_ServeCannotBringItBack(t *testing.T) {
	// REQ-GATEWAY-003 AC3：恢复后之前失败的请求不自动重放。
	// 一个能来回切换的开关会诱使人写出「等它回来再发一次」，那正是 AC3 禁止的。
	// 恢复 = 重启进程，也就是换一个新的 Availability。
	availability := gateway.New()
	availability.Serve()
	availability.Stop()

	if availability.Serve() {
		t.Fatal("已经停止的网关重新进入了服务状态")
	}
	if availability.Serving() {
		t.Error("Serve 把一个已经停止的网关拉回了服务状态")
	}
	if err := availability.Check(); err == nil {
		t.Error("已经停止的网关在 Serve 之后开始放行请求")
	}

	// 重启：换一个新的实例才重新服务。
	restarted := gateway.New()
	if !restarted.Serve() || restarted.Check() != nil {
		t.Error("重启之后的网关无法开始服务")
	}
}

func TestAvailability_StopIsIdempotent(t *testing.T) {
	availability := gateway.New()
	availability.Serve()
	availability.Stop()
	availability.Stop()

	if err := availability.Check(); !apperr.Is(err, apperr.CodeGatewayUnavailable) {
		t.Errorf("重复 Stop 之后 Check 返回 %v", err)
	}
}

func TestAvailability_PublicError_CarriesNoRetryHint(t *testing.T) {
	// 错误里带上「稍后重试」，Agent 就会重试 —— 而 AC3 要求恢复后不重放。
	// 对外文本只能取自码表，这里守的是那条约束在本路径上成立。
	availability := gateway.New()
	availability.Stop()

	public := apperr.PublicOf(availability.Check(), "operation_1")
	if public.Code != apperr.CodeGatewayUnavailable {
		t.Fatalf("对外错误码为 %q", public.Code)
	}
	if public.Message != apperr.New(apperr.CodeGatewayUnavailable).Public().Message {
		t.Errorf("对外文本为 %q，期望码表里的固定文本", public.Message)
	}
	if public.Message != "The gateway is unavailable; the request never reached the external service." {
		t.Errorf("码表文本被改动了：%q", public.Message)
	}
}

func TestAvailability_ConcurrentUse_IsRaceFree(t *testing.T) {
	// 三个接入面会在各自的 goroutine 里读它，关停发生在信号处理的那一个里。
	availability := gateway.New()
	availability.Serve()

	// 每个 goroutine 的结论要么是放行、要么是 gateway_unavailable ——
	// 关停途中不允许出现第三种答复。
	results := make([]error, 16)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = availability.Check()
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		availability.Stop()
	}()
	group.Wait()

	if availability.Serving() {
		t.Error("关停之后仍然在服务状态")
	}
	for index, err := range results {
		if err != nil && !apperr.Is(err, apperr.CodeGatewayUnavailable) {
			t.Errorf("第 %d 个请求拿到了 %v", index, err)
		}
	}
}
