package risk

import "testing"

/*
 * 白盒用例：等级的序与三种抬升方式。
 *
 * 「认不出的等级排在最低」这条性质从外部观察不到 —— validate 已经把非法标签
 * 拦在前面了。但它是 levelOf 返回零值时整条链路仍然安全的原因：一个排在 0 的等级
 * 抬不高任何东西，也覆盖不了任何东西（scope.Covers 用的是同一套序）。
 */

func TestRank_UnknownLevel_SortsBelowEverything(t *testing.T) {
	cases := map[Level]int{
		LevelLow:    1,
		LevelMedium: 2,
		LevelHigh:   3,
		"":          0,
		"critical":  0,
		"LOW":       0,
	}

	for level, expected := range cases {
		if got := rank(level); got != expected {
			t.Errorf("等级 %q 的序为 %d，期望 %d", level, got, expected)
		}
	}
}

func TestRaiseFunctions_MapEveryBaseline(t *testing.T) {
	cases := []struct {
		name     string
		raise    func(Level) Level
		baseline Level
		expected Level
	}{
		{"toHigh 从 low 起", toHigh, LevelLow, LevelHigh},
		{"toHigh 从 high 起", toHigh, LevelHigh, LevelHigh},
		{"atLeastMedium 从 low 起", atLeastMedium, LevelLow, LevelMedium},
		{"atLeastMedium 从 medium 起", atLeastMedium, LevelMedium, LevelMedium},
		{"atLeastMedium 不下调 high", atLeastMedium, LevelHigh, LevelHigh},
		{"oneLevelUp 从 low 起", oneLevelUp, LevelLow, LevelMedium},
		{"oneLevelUp 从 medium 起", oneLevelUp, LevelMedium, LevelHigh},
		{"oneLevelUp 在 high 到顶", oneLevelUp, LevelHigh, LevelHigh},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.raise(testCase.baseline); got != testCase.expected {
				t.Errorf("算出 %s，期望 %s", got, testCase.expected)
			}
		})
	}
}
