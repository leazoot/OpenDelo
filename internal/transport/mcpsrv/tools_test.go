package mcpsrv_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/transport/mcpsrv"
)

/*
 * 工具清单（REQ-MCP-001、REQ-MCP-002）。
 *
 * 清单跑在**四个真实 Adapter** 上，不用简化夹具：这里要验证的正是
 * 「由声明生成」这件事对现有声明成立，用假声明测等于测自己写的假设。
 */

func realRegistry(t *testing.T) *registry.Registry {
	t.Helper()

	adapters, err := allAdapters(t)
	if err != nil {
		t.Fatalf("注册真实 Adapter 失败：%v", err)
	}
	return adapters
}

func realCatalog(t *testing.T) *mcpsrv.Catalog {
	t.Helper()

	catalog, err := mcpsrv.NewCatalog(realRegistry(t))
	if err != nil {
		t.Fatalf("生成工具清单失败：%v", err)
	}
	return catalog
}

func TestNewCatalog_EveryDeclaredCapability_BecomesExactlyOneTool(t *testing.T) {
	// REQ-MCP-001 AC1：清单由声明生成，不存在手写重复定义。
	// 「一一对应」是这句话可检验的形式：多出来的工具没有声明支撑，
	// 少掉的工具意味着某个能力在 MCP 面上凭空消失。
	adapters := realRegistry(t)
	declared := 0
	for _, service := range adapters.Services() {
		adapter, err := adapters.Adapter(service)
		if err != nil {
			t.Fatalf("取 Adapter %s 失败：%v", service, err)
		}
		declared += len(adapter.Capabilities())
	}

	tools := realCatalog(t).Tools()
	if len(tools) != declared {
		t.Errorf("清单里有 %d 个工具，声明里有 %d 个能力", len(tools), declared)
	}
	if declared == 0 {
		t.Fatal("没有任何 Adapter 声明能力，这条用例测不到东西")
	}
}

func TestNewCatalog_EveryToolName_FollowsServiceResourceAction(t *testing.T) {
	// REQ-MCP-001：命名遵循 <service>.<resource>.<action>。
	for _, tool := range realCatalog(t).Tools() {
		parts := strings.Split(tool.Name, ".")
		if len(parts) != 3 {
			t.Errorf("工具名 %q 不是三段", tool.Name)
			continue
		}
		for index, part := range parts {
			if strings.TrimSpace(part) == "" {
				t.Errorf("工具名 %q 的第 %d 段为空", tool.Name, index+1)
			}
		}
	}
}

func TestNewCatalog_ToolNames_StayUniqueAfterTheClientReplacesDots(t *testing.T) {
	// 实测：客户端把点换成下划线之后才交给模型。
	// 线路上不撞名不代表模型那边不撞名。
	seen := make(map[string]string)
	for _, tool := range realCatalog(t).Tools() {
		key := strings.ReplaceAll(tool.Name, ".", "_")
		if previous, taken := seen[key]; taken {
			t.Errorf("%q 与 %q 在替换点号之后都是 %q", tool.Name, previous, key)
		}
		seen[key] = tool.Name
	}
}

func TestNewCatalog_NoToolReadsACredential(t *testing.T) {
	// REQ-MCP-002 AC1。判定按动词：读出来的是现成的凭据，造出来的不是 ——
	// 轮换类操作（update_secret、manage_token）是 PRD 允许的，经审批执行。
	for _, tool := range realCatalog(t).Tools() {
		parts := strings.Split(tool.Name, ".")
		if len(parts) != 3 {
			continue
		}
		resource, action := parts[1], parts[2]
		if action != "read" {
			continue
		}
		for _, word := range []string{"credential", "secret", "token", "vault", "password"} {
			if strings.Contains(resource, word) {
				t.Errorf("工具 %s 是在读取凭据", tool.Name)
			}
		}
	}
}

func TestNewCatalog_RotationTools_SurviveTheCredentialCheck(t *testing.T) {
	// 反向断言：只按名词匹配会把轮换类操作一起关掉，而 PRD 明确允许它们
	// （经审批）。这条用例保证那条规则没有被写成按名词匹配。
	tools := realCatalog(t).Tools()
	wanted := map[string]bool{}
	for _, tool := range tools {
		wanted[tool.Name] = true
	}

	rotations := 0
	for name := range wanted {
		if strings.HasSuffix(name, ".update") || strings.HasSuffix(name, ".manage") {
			if strings.Contains(name, "secret") || strings.Contains(name, "token") {
				rotations++
			}
		}
	}
	if rotations == 0 {
		t.Error("清单里一个凭据轮换类工具都没有，规则可能退回成了按名词匹配")
	}
}

func TestNewCatalog_EveryInputSchema_ComesFromTheDeclaration(t *testing.T) {
	// REQ-MCP-001 AC2：工具的输入 Schema 与 Adapter 声明一致。
	adapters := realRegistry(t)
	catalog := realCatalog(t)

	for _, tool := range catalog.Tools() {
		target, found := catalog.Target(tool.Name)
		if !found {
			t.Fatalf("清单里的 %s 反查不到能力", tool.Name)
		}
		capability, err := adapters.Capability(target.Service, target.Operation)
		if err != nil {
			t.Fatalf("取能力 %s/%s 失败：%v", target.Service, target.Operation, err)
		}

		var fromDeclaration, fromTool any
		if err := json.Unmarshal([]byte(capability.InputSchema), &fromDeclaration); err != nil {
			t.Fatalf("声明里的 Schema 解不开：%v", err)
		}
		if err := json.Unmarshal(tool.InputSchema, &fromTool); err != nil {
			t.Fatalf("工具的 Schema 解不开：%v", err)
		}
		if !jsonEqual(fromDeclaration, fromTool) {
			t.Errorf("%s 的 Schema 与声明不一致", tool.Name)
		}
	}
}

func TestNewCatalog_EveryDescription_SaysTheRiskAndCarriesNothingElse(t *testing.T) {
	// 描述里写风险等级，是为了让模型在调用之前就知道这一次可能需要人工确认；
	// 同时它不得含 BaseURL、路径或声明之外的任何信息。
	for _, tool := range realCatalog(t).Tools() {
		if !strings.HasPrefix(tool.Description, "Risk: ") {
			t.Errorf("%s 的描述没有写风险等级：%s", tool.Name, tool.Description)
		}
		for _, leaked := range []string{"http://", "https://", "/repos/", "/zones/", "Bearer"} {
			if strings.Contains(tool.Description, leaked) {
				t.Errorf("%s 的描述里出现了 %q：%s", tool.Name, leaked, tool.Description)
			}
		}
	}
}

func TestTools_ReturnsACopy(t *testing.T) {
	// 清单是启动时定死的。调用方改到的必须是自己那一份。
	catalog := realCatalog(t)
	first := catalog.Tools()
	if len(first) == 0 {
		t.Fatal("清单为空")
	}

	original := first[0].Name
	first[0].Name = "tampered"
	if catalog.Tools()[0].Name != original {
		t.Error("改动调用方拿到的切片会影响清单本身")
	}
}

func jsonEqual(left, right any) bool {
	leftText, leftErr := json.Marshal(left)
	rightText, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftText) == string(rightText)
}
