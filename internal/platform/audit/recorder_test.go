package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

var recordedAt = time.Date(2026, time.July, 28, 9, 15, 30, 123_000_000, time.UTC)

// fakeRepository 记下收到的事件，或按需失败。
//
// 用 fake 而不是 mock：这里要断言的是「写入器交给仓储的是什么」与
// 「仓储失败时写入器怎么办」，不是调用次数。
type fakeRepository struct {
	audit.Repository

	appended []audit.Event
	failWith error
}

func (f *fakeRepository) Append(_ context.Context, event audit.Event) (audit.Event, error) {
	if f.failWith != nil {
		return audit.Event{}, f.failWith
	}
	f.appended = append(f.appended, event)
	return event, nil
}

func newRecorder(t *testing.T) (*audit.Recorder, *fakeRepository) {
	t.Helper()

	repository := &fakeRepository{}
	recorder, err := audit.NewRecorder(repository, clock.NewFixed(recordedAt), ulid.New(clock.NewFixed(recordedAt)))
	if err != nil {
		t.Fatalf("组装写入器失败：%v", err)
	}
	return recorder, repository
}

func sampleEvent(options ...func(*audit.Event)) audit.Event {
	event := audit.Event{
		OperationID:   "01K1OPERATION00000000000000",
		Type:          audit.EventAutoAllowed,
		Service:       "github",
		Operation:     "repo.read",
		Resource:      `{"repo":"Runcoor/opendelo"}`,
		ResolvedScope: `{"service":"github"}`,
		Outcome:       audit.OutcomeSucceeded,
		Metadata:      `{"match_level":"workspace_binding"}`,
	}
	for _, apply := range options {
		apply(&event)
	}
	return event
}

func assertCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望错误码 %s，但没有出错", want)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) {
		t.Fatalf("错误不是 *apperr.Error：%v", err)
	}
	if appError.Code() != want {
		t.Errorf("错误码是 %s，期望 %s（%v）", appError.Code(), want, err)
	}
}

func TestRecorder_EveryDeclaredEventType_IsWritten(t *testing.T) {
	// REQ-AUDIT-002 AC1：十类事件加需求点名的三个，再加 D-07 授权新增的
	// agent.identity_mismatch 与 agent.trusted、D-14 授权新增的
	// security.strong_auth_locked 与 D-16 授权新增的 trust.cleared，各写一次。
	ctx := t.Context()
	recorder, repository := newRecorder(t)

	types := audit.EventTypes()
	if len(types) != 17 {
		t.Fatalf("事件类型有 %d 个，期望 17 个", len(types))
	}

	for _, eventType := range types {
		if _, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
			event.Type = eventType
		})); err != nil {
			t.Errorf("事件类型 %q 写入失败：%v", eventType, err)
		}
	}

	if len(repository.appended) != len(types) {
		t.Errorf("仓储收到 %d 条，期望 %d 条", len(repository.appended), len(types))
	}
	for index, written := range repository.appended {
		if written.Type != types[index] {
			t.Errorf("第 %d 条的类型是 %q，期望 %q", index, written.Type, types[index])
		}
	}
}

func TestRecorder_UnregisteredEventType_IsRejected(t *testing.T) {
	// 封闭枚举：没登记的类型进不来，前端过滤器因此不会遇到认不出的条目。
	ctx := t.Context()
	recorder, repository := newRecorder(t)

	for _, eventType := range []audit.EventType{"info", "decision", "lease.renewed", ""} {
		_, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
			event.Type = eventType
		}))
		assertCode(t, err, apperr.CodeInvalidRequest)
	}

	if len(repository.appended) != 0 {
		t.Errorf("被拒绝的事件仍写进了仓储：%d 条", len(repository.appended))
	}
}

func TestRecorder_MissingOperationID_IsRejected(t *testing.T) {
	// 没有 operation_id 的记录无从追溯，写下来也帮不上任何人。
	recorder, _ := newRecorder(t)

	_, err := recorder.Record(t.Context(), sampleEvent(func(event *audit.Event) {
		event.OperationID = ""
	}))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestRecorder_RepositoryFailure_PropagatesUnchanged(t *testing.T) {
	// ADR-004：审计写入是执行的前置条件。写不进去必须让调用方拿到错误，
	// 不吞、不降级、不换成一个更温和的码。
	ctx := t.Context()
	repository := &fakeRepository{failWith: apperr.New(apperr.CodeInternal).WithDetail("磁盘满了")}
	recorder, err := audit.NewRecorder(repository, clock.NewFixed(recordedAt), ulid.New(clock.NewFixed(recordedAt)))
	if err != nil {
		t.Fatalf("组装写入器失败：%v", err)
	}

	_, recordErr := recorder.Record(ctx, sampleEvent())
	assertCode(t, recordErr, apperr.CodeInternal)
	if !errors.Is(recordErr, repository.failWith) {
		t.Errorf("仓储的错误没有被原样传递：%v", recordErr)
	}
}

func TestNewRecorder_MissingDependency_IsRejected(t *testing.T) {
	// 缺时钟或 ID 生成器意味着记录无法被定位，那样的写入器不该被造出来。
	cases := []struct {
		name       string
		repository audit.Repository
		source     clock.Clock
		ids        *ulid.Generator
	}{
		{name: "缺仓储", repository: nil, source: clock.NewFixed(recordedAt), ids: ulid.New(clock.System{})},
		{name: "缺时钟", repository: &fakeRepository{}, source: nil, ids: ulid.New(clock.System{})},
		{name: "缺 ID 生成器", repository: &fakeRepository{}, source: clock.NewFixed(recordedAt), ids: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := audit.NewRecorder(testCase.repository, testCase.source, testCase.ids)
			assertCode(t, err, apperr.CodeInvalidConfiguration)
		})
	}
}

func TestRecorder_StampsIdentityAndTimeItself(t *testing.T) {
	// 调用方给的 ID 与时刻一律被忽略：伪造的时间戳会让账本上的先后关系失真。
	ctx := t.Context()
	recorder, repository := newRecorder(t)

	forged := sampleEvent(func(event *audit.Event) {
		event.ID = "forged-id"
		event.CreatedAt = recordedAt.Add(-100 * time.Hour)
	})
	written, err := recorder.Record(ctx, forged)
	if err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	if written.ID == forged.ID {
		t.Error("调用方给的 ID 被采用了")
	}
	if !written.CreatedAt.Equal(recordedAt) {
		t.Errorf("发生时刻是 %v，期望取自网关时钟的 %v", written.CreatedAt, recordedAt)
	}
	if repository.appended[0].ID != written.ID {
		t.Error("落库的 ID 与返回的 ID 不一致")
	}
}

func TestRecorder_RedactsSensitiveKeys_AtEveryDepth(t *testing.T) {
	// PRD §22.2 的词表逐条生效，且对嵌套对象与数组同样生效 ——
	// 一个凭据藏在数组里的第三层，与藏在顶层没有区别。
	ctx := t.Context()
	recorder, repository := newRecorder(t)

	const leak = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"
	written, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
		event.Resource = `{"repo":"opendelo","headers":{"Authorization":"` + leak +
			`","X-Api-Key":"` + leak + `"},"list":[{"set-cookie":"` + leak + `"}]}`
		event.ResolvedScope = `{"nested":{"deep":{"private_key":"` + leak + `"}}}`
		event.Metadata = `{"db_password":"` + leak + `","match_level":"trust_memory"}`
	}))
	if err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	for _, field := range []string{written.Resource, written.ResolvedScope, written.Metadata} {
		if strings.Contains(field, leak) {
			t.Errorf("脱敏后仍含哨兵：%s", field)
		}
		if !strings.Contains(field, audit.Redacted) {
			t.Errorf("字段里没有出现 %s：%s", audit.Redacted, field)
		}
	}

	// 无害字段必须原样保留，否则脱敏就把账本一起抹掉了。
	if !strings.Contains(written.Resource, "opendelo") {
		t.Errorf("无害字段被一起抹掉了：%s", written.Resource)
	}
	if !strings.Contains(written.Metadata, "trust_memory") {
		t.Errorf("无害字段被一起抹掉了：%s", written.Metadata)
	}

	// 落库的与返回的是同一份，不存在「返回时脱敏、落库时没脱」。
	if repository.appended[0].Resource != written.Resource {
		t.Error("落库内容与返回内容不一致")
	}
}

func TestRecorder_EverySensitiveWord_IsCovered(t *testing.T) {
	// 逐条走词表，避免某个词在实现里被漏掉却没人发现。
	ctx := t.Context()
	recorder, _ := newRecorder(t)

	const leak = "SENTINEL_APIKEY_5e9b27_DO_NOT_LEAK"
	for _, word := range audit.SensitiveKeyWords() {
		// 每个词走三种真实会遇到的写法：词本身、带前缀、带后缀并换大小写。
		// 只测词本身的话，把子串匹配改成全等也能通过 —— 而那会让
		// access_token、db_password、X-API-Key 全部漏网。
		for _, key := range []string{word, "x_" + word, strings.ToUpper(word) + "_VALUE"} {
			payload := map[string]string{key: leak}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("构造用例数据失败：%v", err)
			}

			written, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
				event.Metadata = string(encoded)
			}))
			if err != nil {
				t.Fatalf("键 %q 的用例写入失败：%v", key, err)
			}
			if strings.Contains(written.Metadata, leak) {
				t.Errorf("键 %q 没有被脱敏：%s", key, written.Metadata)
			}
		}
	}
}

func TestRecorder_SensitiveWordList_MatchesTheLoggingOne(t *testing.T) {
	// 两条输出路径各留一份词表（互不依赖），但内容必须一致 ——
	// 只在一边加词会让另一边悄悄漏。
	auditWords := audit.SensitiveKeyWords()
	loggingWords := logging.SensitiveKeyWords()

	if len(auditWords) != len(loggingWords) {
		t.Fatalf("审计词表 %d 条，日志词表 %d 条", len(auditWords), len(loggingWords))
	}
	for index, word := range auditWords {
		if word != loggingWords[index] {
			t.Errorf("第 %d 条：审计是 %q，日志是 %q", index, word, loggingWords[index])
		}
	}
}

func TestRecorder_MetadataMustBeFlatScalars(t *testing.T) {
	// 嵌套结构正是「完整响应正文」混进账本的形状。
	// Ledger 的条目详情展示的就是一组键值对，扁平不是限制而是它本来的样子。
	ctx := t.Context()
	recorder, _ := newRecorder(t)

	cases := []struct {
		name     string
		metadata string
		accepted bool
	}{
		{name: "字符串与数字", metadata: `{"level":"low","count":3}`, accepted: true},
		{name: "布尔与空值", metadata: `{"cached":true,"note":null}`, accepted: true},
		{name: "嵌套对象", metadata: `{"response":{"body":"…"}}`, accepted: false},
		{name: "数组", metadata: `{"items":[1,2,3]}`, accepted: false},
		{name: "根不是对象", metadata: `["a"]`, accepted: false},
		{name: "不是 JSON", metadata: `not json`, accepted: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
				event.Metadata = testCase.metadata
			}))
			if testCase.accepted && err != nil {
				t.Errorf("合法的元数据被拒绝：%v", err)
			}
			if !testCase.accepted && err == nil {
				t.Error("不该进账本的元数据被接受了")
			}
		})
	}
}

func TestRecorder_LongMetadataValue_IsTruncatedNotStored(t *testing.T) {
	// 超长字符串不是键值明细，是正文。截断而不是拒绝：
	// 一条审计事件不该因为附带信息太长就写不进去（那等于请求失败）。
	ctx := t.Context()
	recorder, _ := newRecorder(t)

	body := strings.Repeat("A", 4096)
	written, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
		event.Metadata = `{"note":"` + body + `"}`
	}))
	if err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal([]byte(written.Metadata), &decoded); err != nil {
		t.Fatalf("解析元数据失败：%v", err)
	}
	if len(decoded["note"]) >= len(body) {
		t.Errorf("超长值没有被截断，长度为 %d", len(decoded["note"]))
	}
	if !strings.HasSuffix(decoded["note"], audit.Truncated) {
		t.Errorf("截断后没有留下标记：%q", decoded["note"])
	}
}

func TestRecorder_MalformedResource_IsRejected(t *testing.T) {
	ctx := t.Context()
	recorder, repository := newRecorder(t)

	for _, resource := range []string{"not json", `["array"]`, `"string"`} {
		_, err := recorder.Record(ctx, sampleEvent(func(event *audit.Event) {
			event.Resource = resource
		}))
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
	if len(repository.appended) != 0 {
		t.Errorf("被拒绝的事件仍写进了仓储：%d 条", len(repository.appended))
	}
}
