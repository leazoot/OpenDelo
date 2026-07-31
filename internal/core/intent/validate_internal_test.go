package intent

import (
	"strings"
	"testing"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 输出校验的白盒用例（REQ-INTENT-001 AC2）。
 *
 * 这一层是最后一道防线：能力映射表的检查已经挡掉了绝大多数缺字段的情况，
 * 从包外几乎构造不出一个只缺资源标识的意图。但「几乎」不是「不可能」——
 * 表的检查将来若被放宽，或多出一条不经 extractResource 的构造路径，
 * 就要靠这里拦住。用内部用例逐个分支验，否则这些分支等于没有被测过。
 */

func completeIntent() Intent {
	return Intent{
		Service:     "cloudflare",
		Operation:   "dns.record.update",
		Resource:    map[string]string{"zone": "tele-call.cn"},
		ResourceKey: "zone=tele-call.cn",
		Environment: matcher.EnvironmentProduction,
		RiskLabel:   adapters.RiskLabelMedium,
	}
}

func TestIntentValidate_CompleteIntent_IsAccepted(t *testing.T) {
	// 正向对照：少了它，一个「什么都拒绝」的实现也会让下面全绿。
	if err := completeIntent().validate(); err != nil {
		t.Fatalf("完整的意图被拒：%v", err)
	}
}

func TestIntentValidate_EveryMissingPiece_IsRefused(t *testing.T) {
	cases := []struct {
		name   string
		blank  func(*Intent)
		reason string
	}{
		{"没有服务", func(i *Intent) { i.Service = "" }, "没有服务"},
		{"没有操作", func(i *Intent) { i.Operation = "" }, "没有操作"},
		{"没有资源字段", func(i *Intent) { i.Resource = nil }, "没有资源标识"},
		{"没有资源标识文本", func(i *Intent) { i.ResourceKey = "" }, "没有资源标识"},
		{"环境为空", func(i *Intent) { i.Environment = "" }, "环境取值不合法"},
		{"环境取值不在集合里", func(i *Intent) { i.Environment = "staging" }, "环境取值不合法"},
		{"风险标签为空", func(i *Intent) { i.RiskLabel = "" }, "风险标签不合法"},
		{"风险标签不在集合里", func(i *Intent) { i.RiskLabel = "critical" }, "风险标签不合法"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolved := completeIntent()
			testCase.blank(&resolved)

			err := resolved.validate()
			if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
				t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
			}
			// 八个分支返回同一个码，只断言码等于没有区分它们。
			if !strings.Contains(err.Error(), testCase.reason) {
				t.Errorf("拒绝的理由是 %q，期望提到 %q", err, testCase.reason)
			}
		})
	}
}
