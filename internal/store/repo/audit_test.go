package repo_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/audit"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

// auditChain 在决策链之外加上账本仓储。
type auditChain struct {
	chain  requestChain
	events *repo.AuditEvents
}

func newAuditChain(t *testing.T) auditChain {
	t.Helper()

	chain := seededDecisionChain(t)
	if _, err := chain.decisions.CreateDecision(t.Context(), fixtures.Decision()); err != nil {
		t.Fatalf("写入决策失败：%v", err)
	}
	return auditChain{chain: chain, events: repo.NewAuditEvents(chain.db)}
}

func TestAuditEvents_AppendThenRead_RoundTripsEveryField(t *testing.T) {
	ctx := t.Context()
	all := newAuditChain(t)
	want := fixtures.Event()

	appended, err := all.events.Append(ctx, want)
	if err != nil {
		t.Fatalf("写入审计事件失败：%v", err)
	}
	if appended != want {
		t.Errorf("写入返回 %+v，期望 %+v", appended, want)
	}

	byID, err := all.events.EventByID(ctx, want.ID)
	if err != nil {
		t.Fatalf("按主键读取失败：%v", err)
	}
	if byID != want {
		t.Errorf("按主键读到 %+v，期望 %+v", byID, want)
	}
}

func TestAuditEvents_RepositoryExposesNoUpdatePath(t *testing.T) {
	// 账本只追加。改写一条记录等于改写「当时发生了什么」，
	// 那样它就不再是账本了。唯一的删除入口是按时间条件的清理。
	repository := reflect.TypeOf((*audit.Repository)(nil)).Elem()

	forbidden := []string{"update", "set", "modify", "patch", "edit", "truncate", "clear"}
	for index := range repository.NumMethod() {
		name := strings.ToLower(repository.Method(index).Name)
		for _, word := range forbidden {
			if strings.Contains(name, word) {
				t.Errorf("audit.Repository 暴露了 %s，账本不接受改写", repository.Method(index).Name)
			}
		}
		if strings.Contains(name, "delete") || (strings.Contains(name, "prune") && name != "prunebefore") {
			t.Errorf("audit.Repository 暴露了 %s，删除只允许按保留期执行", repository.Method(index).Name)
		}
	}

	// 反向对照：确认这条断言扫的是真实的方法集，不是空集合。
	if repository.NumMethod() == 0 {
		t.Fatal("接口没有任何方法，这条断言退化成了永真")
	}
	if _, found := repository.MethodByName("PruneBefore"); !found {
		t.Error("保留期清理入口不见了，超期记录将无法删除")
	}
}

func TestAuditEvents_UnidentifiedRequest_RoundTripsAsEmpty(t *testing.T) {
	// 认不出请求者的拒绝也必须留下记录 —— 写不进去就等于一条未审计的路径。
	ctx := t.Context()
	all := newAuditChain(t)

	blocked := fixtures.Event(
		fixtures.WithEventType(audit.EventDenied),
		fixtures.WithEventOutcome(audit.OutcomeBlocked),
		fixtures.WithEventResponseStatus(0),
		fixtures.WithEventUnidentified(),
	)
	appended, err := all.events.Append(ctx, blocked)
	if err != nil {
		t.Fatalf("写入认不出请求者的事件失败：%v", err)
	}
	if appended.AgentID != "" || appended.DecisionID != "" || appended.ResponseStatus != 0 {
		t.Errorf("空字段被填上了内容：%+v", appended)
	}

	stored, err := all.events.EventByID(ctx, blocked.ID)
	if err != nil {
		t.Fatalf("读取事件失败：%v", err)
	}
	if stored != blocked {
		t.Errorf("读到 %+v，期望 %+v", stored, blocked)
	}
}

func TestAuditEvents_UnknownAgent_ReportsInvalidRequest(t *testing.T) {
	// 留空是允许的，指向一个不存在的 Agent 不是 —— 账本里的引用必须是真的。
	all := newAuditChain(t)

	_, err := all.events.Append(t.Context(),
		fixtures.Event(fixtures.WithEventAgentID("01K1MISSING00000000000000")))
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestAuditEvents_ListedNewestFirst_AndPagedByCursor(t *testing.T) {
	ctx := t.Context()
	all := newAuditChain(t)

	moments := []time.Time{
		fixtures.Instant,
		fixtures.Instant.Add(time.Minute),
		fixtures.Instant.Add(2 * time.Minute),
	}
	for index, moment := range moments {
		if _, err := all.events.Append(ctx, fixtures.Event(
			fixtures.WithEventID("01K1EVENT0000000000000000"+string(rune('1'+index))),
			fixtures.WithEventCreatedAt(moment),
		)); err != nil {
			t.Fatalf("写入第 %d 条事件失败：%v", index, err)
		}
	}

	// 零值游标表示从最新一条开始。
	newest, err := all.events.Events(ctx, time.Time{}, 2)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	if len(newest) != 2 {
		t.Fatalf("第一页有 %d 条，期望 2 条", len(newest))
	}
	if !newest[0].CreatedAt.Equal(moments[2]) || !newest[1].CreatedAt.Equal(moments[1]) {
		t.Errorf("第一页不是按时间倒序：%v, %v", newest[0].CreatedAt, newest[1].CreatedAt)
	}

	// 用最后一条的时刻作游标翻下一页。
	next, err := all.events.Events(ctx, newest[1].CreatedAt, 2)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	if len(next) != 1 || !next[0].CreatedAt.Equal(moments[0]) {
		t.Errorf("第二页有 %d 条，期望剩下的那一条", len(next))
	}
}

func TestAuditEvents_FilteredByAgentAndService(t *testing.T) {
	ctx := t.Context()
	all := newAuditChain(t)

	if _, err := all.events.Append(ctx, fixtures.Event()); err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}
	if _, err := all.events.Append(ctx, fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000002"),
		fixtures.WithEventService("cloudflare"),
		fixtures.WithEventUnidentified(),
	)); err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}

	byAgent, err := all.events.EventsByAgent(ctx, fixtures.DefaultAgentID, time.Time{}, 10)
	if err != nil {
		t.Fatalf("按 Agent 过滤失败：%v", err)
	}
	if len(byAgent) != 1 || byAgent[0].Service != fixtures.DefaultServiceLabel {
		t.Errorf("按 Agent 过滤到 %d 条", len(byAgent))
	}

	byService, err := all.events.EventsByService(ctx, "cloudflare", time.Time{}, 10)
	if err != nil {
		t.Fatalf("按服务过滤失败：%v", err)
	}
	if len(byService) != 1 || byService[0].AgentID != "" {
		t.Errorf("按服务过滤到 %d 条", len(byService))
	}
}

func TestAuditEvents_ListQueries_RejectNonPositiveLimit(t *testing.T) {
	all := newAuditChain(t)
	ctx := t.Context()

	for _, limit := range []int{0, -1} {
		_, err := all.events.Events(ctx, time.Time{}, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)

		_, err = all.events.EventsByAgent(ctx, fixtures.DefaultAgentID, time.Time{}, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)

		_, err = all.events.EventsByService(ctx, fixtures.DefaultServiceLabel, time.Time{}, limit)
		assertCode(t, err, apperr.CodeInvalidRequest)
	}
}

func TestAuditEvents_Prune_OnlyRemovesRecordsBeforeTheCutoff(t *testing.T) {
	// REQ-AUDIT-005 AC1：清理只删除超过保留期的记录。
	ctx := t.Context()
	all := newAuditChain(t)

	old := fixtures.Event(fixtures.WithEventCreatedAt(fixtures.Instant))
	recent := fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000002"),
		fixtures.WithEventCreatedAt(fixtures.Instant.Add(time.Hour)),
	)
	for _, event := range []audit.Event{old, recent} {
		if _, err := all.events.Append(ctx, event); err != nil {
			t.Fatalf("写入事件失败：%v", err)
		}
	}

	cutoff := fixtures.Instant.Add(30 * time.Minute)
	counted, err := all.events.CountBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("统计超期记录失败：%v", err)
	}
	if counted != 1 {
		t.Errorf("统计出 %d 条超期记录，期望 1 条", counted)
	}

	record := fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000009"),
		fixtures.WithEventType(audit.EventPruned),
		fixtures.WithEventCreatedAt(fixtures.Instant.Add(2*time.Hour)),
	)
	pruned, written, err := all.events.PruneBefore(ctx, cutoff, record)
	if err != nil {
		t.Fatalf("清理超期记录失败：%v", err)
	}
	if pruned != 1 {
		t.Errorf("清理了 %d 条，期望 1 条", pruned)
	}
	if written.Type != audit.EventPruned {
		t.Errorf("清理事件的类型是 %q，期望 audit.pruned", written.Type)
	}

	// 剩下的是未超期的那条，加上刚写下的清理事件本身。
	remaining, err := all.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("清理后剩下 %d 条，期望 2 条", len(remaining))
	}
	if remaining[0].ID != record.ID || remaining[1].ID != recent.ID {
		t.Errorf("剩下的是 %q 与 %q", remaining[0].ID, remaining[1].ID)
	}
}

func TestAuditEvents_Prune_LeavesNothingBehindWhenTheRecordFails(t *testing.T) {
	// 删除与记录在同一个事务里：清理事件写不进去时，删除必须一起回滚。
	// 否则账本会少掉一段没人知道被删过的历史。
	ctx := t.Context()
	all := newAuditChain(t)

	if _, err := all.events.Append(ctx, fixtures.Event(
		fixtures.WithEventCreatedAt(fixtures.Instant))); err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}

	// 让清理事件本身写不进去：指向一个不存在的 Agent。
	doomed := fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000008"),
		fixtures.WithEventType(audit.EventPruned),
		fixtures.WithEventAgentID("01K1MISSING00000000000000"),
		fixtures.WithEventCreatedAt(fixtures.Instant.Add(2*time.Hour)),
	)
	cutoff := fixtures.Instant.Add(30 * time.Minute)
	if _, _, err := all.events.PruneBefore(ctx, cutoff, doomed); err == nil {
		t.Fatal("清理事件写不进去时，清理却成功了")
	}

	remaining, err := all.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("回滚后剩下 %d 条，期望原来的 1 条一条不少", len(remaining))
	}
}

func TestAuditEvents_Prune_RefusesARecordInsideTheDeletedWindow(t *testing.T) {
	// 清理事件自己落在保留期外，写进去下一轮就被删掉，账本上等于没记过。
	ctx := t.Context()
	all := newAuditChain(t)

	stale := fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000007"),
		fixtures.WithEventType(audit.EventPruned),
		fixtures.WithEventCreatedAt(fixtures.Instant),
	)
	_, _, err := all.events.PruneBefore(ctx, fixtures.Instant.Add(time.Hour), stale)
	assertCode(t, err, apperr.CodeInvalidRequest)
}

func TestAuditEvents_Prune_RefusesAZeroCutoff(t *testing.T) {
	// 零值时刻编码出来看起来「什么都不会删」，但把边界交给巧合不是安全边界该有的样子。
	ctx := t.Context()
	all := newAuditChain(t)
	if _, err := all.events.Append(ctx, fixtures.Event()); err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}

	_, _, err := all.events.PruneBefore(ctx, time.Time{}, fixtures.Event(
		fixtures.WithEventID("01K1EVENT00000000000000006"),
		fixtures.WithEventType(audit.EventPruned),
	))
	assertCode(t, err, apperr.CodeInvalidRequest)

	_, err = all.events.CountBefore(ctx, time.Time{})
	assertCode(t, err, apperr.CodeInvalidRequest)

	remaining, err := all.events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("列出审计事件失败：%v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("被拒绝的清理动了数据：还剩 %d 条", len(remaining))
	}
}

func TestAuditEvents_ThroughTheCoreInterface_Works(t *testing.T) {
	ctx := t.Context()
	all := newAuditChain(t)

	var events audit.Repository = all.events
	if _, err := events.Append(ctx, fixtures.Event()); err != nil {
		t.Fatalf("经接口写入事件失败：%v", err)
	}

	listed, err := events.Events(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("经接口列出事件失败：%v", err)
	}
	if len(listed) != 1 || listed[0].Type != audit.EventAutoAllowed {
		t.Errorf("经接口读到 %d 条", len(listed))
	}
}
