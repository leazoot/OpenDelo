package model_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/model"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * 统一执行契约的用例。
 *
 * 这层只做形状转换，因此风险只有一种：某个字段没被传下去。而漏传预算是其中
 * 最安静的一种 —— 组装根照常拿到结果，账本上这次调用与正常调用长得一模一样，
 * 只有账单知道发生了什么。
 */

func TestExecuteCapability_CarriesTheBudget_SoOverspendIsStillRefused(t *testing.T) {
	fake := newFakeService(t, http.StatusOK, `{"id":"c1","model":"gpt-4o-mini"}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	cases := []struct {
		name    string
		budget  registry.ModelBudget
		spent   registry.ModelUsage
		refused bool
	}{
		{"没有给出预算", registry.ModelBudget{}, registry.ModelUsage{}, true},
		{"费用上限不够", registry.ModelBudget{MaxCostMicros: 1, MaxRequests: 5}, registry.ModelUsage{}, true},
		{"次数已经用完", registry.ModelBudget{MaxCostMicros: 1 << 40, MaxRequests: 10}, registry.ModelUsage{Requests: 10}, true},
		{"预算够", registry.ModelBudget{MaxCostMicros: 1 << 40, MaxRequests: 10}, registry.ModelUsage{}, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := adapter.ExecuteCapability(t.Context(), registry.ExecuteInput{
				Operation: "create_completion", Input: json.RawMessage(completionInput),
				Budget: testCase.budget, Spent: testCase.spent,
				Credential: credential(t), OperationID: operationID,
			})

			if testCase.refused {
				if !apperr.Is(err, apperr.CodeForbidden) {
					t.Fatalf("错误码为 %s，期望 forbidden（%v）", apperr.CodeOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("预算够却被拒绝：%v", err)
			}
			if output.Usage.Requests == 0 {
				t.Error("用量没有回到统一结果里 —— Lease 的计数会因此不动")
			}
		})
	}
}

func TestExecuteCapability_UndeclaredOperation_IsRefused(t *testing.T) {
	// 转换层不得放宽 REQ-ADAPTER-001 AC2：未声明的操作无法被调用。
	fake := newFakeService(t, http.StatusOK, `{}`)
	adapter := newAdapter(t, model.ProviderOpenAI, fake.URL)

	_, err := adapter.ExecuteCapability(t.Context(), registry.ExecuteInput{
		Operation: "delete_organization", Credential: credential(t), OperationID: operationID,
		Budget: registry.ModelBudget{MaxCostMicros: 1 << 40, MaxRequests: 10},
	})
	if !apperr.Is(err, apperr.CodeCapabilityNotOffered) {
		t.Fatalf("错误码为 %s，期望 capability_not_offered（%v）", apperr.CodeOf(err), err)
	}
}
