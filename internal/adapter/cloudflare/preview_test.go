package cloudflare_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/cloudflare"
	"github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 查勘契约的用例。
 *
 * Preview 本身的行为在 adapter_test.go 里已经有用例。本文件守的是它接进
 * 决策链路的那一层：报出来的那个来源操作必须是**已声明的只读操作**，
 * 而查回来的旧值必须只包含对照字段 —— 外部服务在同一条记录里回显的任何
 * 别的东西都不该跟着进审批页面。
 */

func TestPreviewSource_OnlyOperationsThatChangeAnExistingRecordHaveAnOldValue(t *testing.T) {
	adapter := newAdapter(t, "https://api.cloudflare.example")

	cases := []struct {
		name      string
		operation string
		expected  string
	}{
		{"改一条已有记录要先查它", "update_dns_record", "read_dns_record"},
		{"删一条已有记录要先查它", "delete_dns_record", "read_dns_record"},
		{"新建时那条记录还不存在", "create_dns_record", ""},
		{"清缓存影响范围无法枚举", "purge_cache", ""},
		{"读取本身没有旧值可对照", "read_dns_record", ""},
		{"认不出的操作不查勘", "drop_zone", ""},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			if got := adapter.PreviewSource(each.operation); got != each.expected {
				t.Errorf("查勘来源为 %q，期望 %q", got, each.expected)
			}
		})
	}
}

func TestPreviewSource_IsAlwaysADeclaredReadOnlyOperation(t *testing.T) {
	// 这一条把「报出来的操作是只读的」钉在能力声明上，而不是钉在
	// read_dns_record 这个名字上：改名或改方法都会让这里失败。
	adapter := newAdapter(t, "https://api.cloudflare.example")
	all, err := registry.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	sourced := 0
	for _, operation := range all.Operations(cloudflare.Service) {
		source := adapter.PreviewSource(operation)
		if source == "" {
			continue
		}
		sourced++

		capability, capErr := all.Capability(cloudflare.Service, source)
		if capErr != nil {
			t.Fatalf("%s 的查勘来源 %s 没有被声明过：%v", operation, source, capErr)
		}
		if capability.Write() {
			t.Errorf("%s 的查勘来源 %s 是 %s 请求，它会改变外部状态",
				operation, source, capability.Method)
		}
	}
	if sourced == 0 {
		t.Fatal("没有任何操作报出查勘来源 —— 这个用例什么也没验证")
	}
}

func TestPreviewCapability_ReturnsOnlyTheComparedFieldsAndNeverTheCredential(t *testing.T) {
	// 外部服务在同一条记录里回显了一个哨兵。旧值只走对照字段白名单，
	// 因此它到不了审批页面（REQ-NFR-002 的八个面之一：审批信息）。
	fake := newFakeCloudflare(t, http.StatusOK, `{"success":true,"result":{`+
		`"id":"record_1","name":"www.example.com","type":"A","content":"203.0.113.10",`+
		`"ttl":300,"api_token":"`+sentinel.SentinelToken+`"}}`)
	adapter := newAdapter(t, fake.URL)

	preview, err := adapter.PreviewCapability(t.Context(), registry.PreviewInput{
		Operation: "update_dns_record", Resource: resource(),
		Input:      json.RawMessage(`{"type":"A","name":"www.example.com","content":"198.51.100.7"}`),
		Credential: credential(t), OperationID: operationID,
	})
	if err != nil {
		t.Fatalf("查勘失败：%v", err)
	}

	if got := <-fake.exchanges; got.method != http.MethodGet {
		t.Fatalf("查勘发出了 %s，它只该读", got.method)
	}

	content := ""
	for _, change := range preview.Changes {
		if change.Field == "api_token" {
			t.Errorf("对照字段之外的 %s 进了查勘结果", change.Field)
		}
		if change.Field == "content" {
			content = change.Before
		}
	}
	if content != "203.0.113.10" {
		t.Errorf("content 的旧值为 %q，期望外部服务里的那个值", content)
	}

	encoded, err := json.Marshal(preview.Changes)
	if err != nil {
		t.Fatalf("查勘结果无法序列化：%v", err)
	}
	for _, value := range sentinel.All() {
		if strings.Contains(string(encoded), value) {
			t.Errorf("查勘结果里出现了凭据哨兵：%s", encoded)
		}
	}
}
