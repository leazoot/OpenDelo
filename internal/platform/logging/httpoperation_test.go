package logging_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/logging"
)

/*
 * operation_id 的 HTTP 入口。
 *
 * 三个接入面共用这一份。它坏掉的后果不是「日志少一个字段」：`core/pipeline`
 * 把空的 operation_id 判为输入不成立，那个面上的每一次调用都会被拒。
 */

// stubIDs 按顺序发出预先准备好的 ID，用完之后报错。
type stubIDs struct {
	values []string
	err    error
}

func (s *stubIDs) NewID() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if len(s.values) == 0 {
		return "", errors.New("没有更多 ID 了")
	}
	next := s.values[0]
	s.values = s.values[1:]
	return next, nil
}

func TestWithHTTPOperationID_PutsAFreshIDInTheContextAndTheResponse(t *testing.T) {
	var seen string
	handler := logging.WithHTTPOperationID(
		&stubIDs{values: []string{"01OPERATION"}},
		refuseUnexpectedly(t),
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = logging.OperationIDFrom(r.Context())
		}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if seen != "01OPERATION" {
		t.Errorf("处理器从 context 里取到的是 %q，期望 %q", seen, "01OPERATION")
	}
	if got := recorder.Header().Get(logging.HeaderOperationID); got != "01OPERATION" {
		t.Errorf("响应头里的 operation_id 是 %q，期望 %q", got, "01OPERATION")
	}
}

// TestWithHTTPOperationID_AnExistingIDIsKept：一次请求在整条链路上只能有一个
// operation_id，换一个等于把「同一次请求」在账本里切成两半。
func TestWithHTTPOperationID_AnExistingIDIsKept(t *testing.T) {
	var seen string
	handler := logging.WithHTTPOperationID(
		&stubIDs{values: []string{"01FRESH"}},
		refuseUnexpectedly(t),
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = logging.OperationIDFrom(r.Context())
		}))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request = request.WithContext(logging.WithOperationID(request.Context(), "01UPSTREAM"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if seen != "01UPSTREAM" {
		t.Errorf("上游给过的 ID 被换成了 %q", seen)
	}
	if got := recorder.Header().Get(logging.HeaderOperationID); got != "01UPSTREAM" {
		t.Errorf("响应头里的 operation_id 是 %q，期望沿用上游的", got)
	}
}

// TestWithHTTPOperationID_WhenTheIDCannotBeMade_TheRequestIsRefused：
// 拿不到 ID 意味着这次操作无法被审计追溯，而审计是执行的前置条件（ADR-004）。
func TestWithHTTPOperationID_WhenTheIDCannotBeMade_TheRequestIsRefused(t *testing.T) {
	reached := false
	refused := false

	handler := logging.WithHTTPOperationID(
		&stubIDs{err: errors.New("时钟倒流")},
		func(w http.ResponseWriter, _ *http.Request, _ error) {
			refused = true
			w.WriteHeader(http.StatusInternalServerError)
		},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if reached {
		t.Error("生成 ID 失败后请求仍然到了处理器 —— 这次调用将无法被追溯")
	}
	if !refused {
		t.Error("生成 ID 失败时没有走拒绝路径")
	}
	if recorder.Header().Get(logging.HeaderOperationID) != "" {
		t.Error("生成失败却仍然写出了 operation_id 响应头")
	}
}

func refuseUnexpectedly(t *testing.T) logging.RefuseFunc {
	t.Helper()

	return func(http.ResponseWriter, *http.Request, error) {
		t.Error("不该走到拒绝路径")
	}
}
