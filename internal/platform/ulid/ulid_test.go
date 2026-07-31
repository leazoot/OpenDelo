package ulid_test

import (
	"sync"
	"testing"
	"time"

	oklog "github.com/oklog/ulid/v2"

	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

var anchor = time.Date(2026, time.July, 27, 9, 15, 30, 123_000_000, time.UTC)

func mustGenerate(t *testing.T, generator *ulid.Generator) string {
	t.Helper()

	id, err := generator.NewID()
	if err != nil {
		t.Fatalf("生成 ULID 失败：%v", err)
	}
	return id
}

func TestGenerator_NewID_WithinSameMillisecond_IsLexicographicallyIncreasing(t *testing.T) {
	// AC1 最严的情况：时钟完全不走，只能靠单调熵源保证有序。
	generator := ulid.New(clock.NewFixed(anchor))

	previous := mustGenerate(t, generator)
	for i := range 1000 {
		current := mustGenerate(t, generator)
		if current <= previous {
			t.Fatalf("第 %d 个 ID %q 未大于前一个 %q", i+1, current, previous)
		}
		previous = current
	}
}

func TestGenerator_NewID_AcrossAdvancingClock_IsLexicographicallyIncreasing(t *testing.T) {
	fixed := clock.NewFixed(anchor)
	generator := ulid.New(fixed)

	previous := mustGenerate(t, generator)
	for i := range 200 {
		fixed.Advance(time.Millisecond)
		current := mustGenerate(t, generator)
		if current <= previous {
			t.Fatalf("第 %d 个 ID %q 未大于前一个 %q", i+1, current, previous)
		}
		previous = current
	}
}

func TestGenerator_NewID_WithSystemClock_IsLexicographicallyIncreasing(t *testing.T) {
	generator := ulid.New(clock.System{})

	previous := mustGenerate(t, generator)
	for i := range 500 {
		current := mustGenerate(t, generator)
		if current <= previous {
			t.Fatalf("第 %d 个 ID %q 未大于前一个 %q", i+1, current, previous)
		}
		previous = current
	}
}

func TestGenerator_NewID_EncodesTimeFromInjectedClock(t *testing.T) {
	// AC2：时钟推进多少，ID 里的时间戳就前进多少 —— 这是后续用例能断言
	// 「Lease 15 分钟后过期」的基础。
	fixed := clock.NewFixed(anchor)
	generator := ulid.New(fixed)

	before := parseTimestamp(t, mustGenerate(t, generator))
	if !before.Equal(anchor.Truncate(time.Millisecond)) {
		t.Errorf("ID 中的时间为 %v，期望 %v", before, anchor)
	}

	fixed.Advance(15 * time.Minute)

	after := parseTimestamp(t, mustGenerate(t, generator))
	if elapsed := after.Sub(before); elapsed != 15*time.Minute {
		t.Errorf("时钟推进 15 分钟，ID 中的时间只前进了 %v", elapsed)
	}
}

func TestGenerator_NewID_ReturnsParsableCanonicalForm(t *testing.T) {
	id := mustGenerate(t, ulid.New(clock.System{}))

	if len(id) != 26 {
		t.Errorf("ID 长度为 %d，ULID 的规范形式是 26 个字符：%q", len(id), id)
	}
	if _, err := oklog.ParseStrict(id); err != nil {
		t.Errorf("ID %q 不是合法 ULID：%v", id, err)
	}
}

func TestGenerator_NewID_Concurrent_ProducesUniqueIDs(t *testing.T) {
	const goroutines, perGoroutine = 8, 200

	generator := ulid.New(clock.NewFixed(anchor))
	collected := make(chan string, goroutines*perGoroutine)

	var running sync.WaitGroup
	for range goroutines {
		running.Add(1)
		go func() {
			defer running.Done()
			for range perGoroutine {
				id, err := generator.NewID()
				if err != nil {
					t.Errorf("生成 ULID 失败：%v", err)
					return
				}
				collected <- id
			}
		}()
	}
	running.Wait()
	close(collected)

	seen := make(map[string]bool, goroutines*perGoroutine)
	for id := range collected {
		if seen[id] {
			t.Fatalf("ID %q 重复", id)
		}
		seen[id] = true
	}
	if len(seen) != goroutines*perGoroutine {
		t.Errorf("收到 %d 个 ID，期望 %d 个", len(seen), goroutines*perGoroutine)
	}
}

func parseTimestamp(t *testing.T, id string) time.Time {
	t.Helper()

	parsed, err := oklog.ParseStrict(id)
	if err != nil {
		t.Fatalf("解析 ULID %q 失败：%v", id, err)
	}
	return oklog.Time(parsed.Time()).UTC()
}
