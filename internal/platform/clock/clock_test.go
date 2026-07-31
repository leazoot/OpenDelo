package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/clock"
)

var anchor = time.Date(2026, time.July, 27, 9, 15, 30, 123_000_000, time.UTC)

func TestSystem_Now_ReturnsUTCAndAdvances(t *testing.T) {
	system := clock.System{}

	first := system.Now()
	if first.Location() != time.UTC {
		t.Errorf("Now() 的时区是 %v，期望 UTC", first.Location())
	}
	if first.IsZero() {
		t.Error("Now() 返回零值时间")
	}
	if second := system.Now(); second.Before(first) {
		t.Errorf("两次 Now() 时间倒退：%v → %v", first, second)
	}
}

func TestFixed_Now_StaysPutUntilAdvanced(t *testing.T) {
	// AC2：固定时钟不会自己走，用例因此可以断言精确的时间点。
	fixed := clock.NewFixed(anchor)

	for range 3 {
		if got := fixed.Now(); !got.Equal(anchor) {
			t.Fatalf("Now() = %v，期望停在 %v", got, anchor)
		}
	}
}

func TestFixed_Advance_MovesExactlyTheRequestedDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"一毫秒":   time.Millisecond,
		"十五分钟":  15 * time.Minute,
		"一百八十天": 180 * 24 * time.Hour,
		"时钟回拨":  -time.Hour,
		"零推进":   0,
	}

	for name, elapsed := range cases {
		t.Run(name, func(t *testing.T) {
			fixed := clock.NewFixed(anchor)

			fixed.Advance(elapsed)

			if want := anchor.Add(elapsed); !fixed.Now().Equal(want) {
				t.Errorf("Advance(%v) 后 Now() = %v，期望 %v", elapsed, fixed.Now(), want)
			}
		})
	}
}

func TestFixed_Advance_Accumulates(t *testing.T) {
	fixed := clock.NewFixed(anchor)

	fixed.Advance(10 * time.Minute)
	fixed.Advance(5 * time.Minute)

	if want := anchor.Add(15 * time.Minute); !fixed.Now().Equal(want) {
		t.Errorf("Now() = %v，期望 %v", fixed.Now(), want)
	}
}

func TestFixed_Set_JumpsToInstantAndNormalizesToUTC(t *testing.T) {
	fixed := clock.NewFixed(anchor)
	shanghai := time.FixedZone("CST", 8*60*60)
	target := time.Date(2026, time.December, 1, 0, 0, 0, 0, shanghai)

	fixed.Set(target)

	if !fixed.Now().Equal(target) {
		t.Errorf("Now() = %v，期望 %v", fixed.Now(), target)
	}
	if fixed.Now().Location() != time.UTC {
		t.Errorf("Now() 的时区是 %v，期望归一化为 UTC", fixed.Now().Location())
	}
}

func TestNewFixed_NormalizesStartToUTC(t *testing.T) {
	fixed := clock.NewFixed(anchor.In(time.FixedZone("CST", 8*60*60)))

	if fixed.Now().Location() != time.UTC {
		t.Errorf("Now() 的时区是 %v，期望 UTC", fixed.Now().Location())
	}
	if !fixed.Now().Equal(anchor) {
		t.Errorf("Now() = %v，期望 %v", fixed.Now(), anchor)
	}
}

func TestFixed_ConcurrentReadAndAdvance_IsRaceFree(t *testing.T) {
	// 决策链路会在多个 goroutine 中读时钟，用例必须能并发驱动它。
	fixed := clock.NewFixed(anchor)

	var waiting sync.WaitGroup
	for range 8 {
		waiting.Add(2)
		go func() {
			defer waiting.Done()
			fixed.Advance(time.Millisecond)
		}()
		go func() {
			defer waiting.Done()
			_ = fixed.Now()
		}()
	}
	waiting.Wait()

	if want := anchor.Add(8 * time.Millisecond); !fixed.Now().Equal(want) {
		t.Errorf("Now() = %v，期望 %v", fixed.Now(), want)
	}
}
