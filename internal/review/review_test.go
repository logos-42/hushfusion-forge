package review

import (
	"context"
	"strings"
	"testing"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestRuleReviewerApprovesCompleteRequest(t *testing.T) {
	rv := NewRuleReviewer()
	v, err := rv.Review(context.Background(), Request{
		ID: "r1", Kind: KindPlan, Subject: "goal-1",
		Summary: "补低温数据", Evidence: []string{"os/TASKS.md 有 T-09", "回执 settled"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Approved {
		t.Fatalf("完整请求应被批准: %v", v.Reasons)
	}
}

func TestRuleReviewerRejectsWithReasons(t *testing.T) {
	rv := NewRuleReviewer()
	v, _ := rv.Review(context.Background(), Request{
		ID: "r2", Kind: KindPlan, Subject: "goal-1", Summary: "", Evidence: []string{"-", "无"},
	})
	if v.Approved {
		t.Fatal("缺摘要+假证据应被拒")
	}
	joined := strings.Join(v.Reasons, "; ")
	if !strings.Contains(joined, "摘要为空") || !strings.Contains(joined, "证据") {
		t.Fatalf("理由应覆盖各失败项: %s", joined)
	}
}

func TestRuleReviewerGoalNeedsCriteria(t *testing.T) {
	rv := NewRuleReviewer()
	v, _ := rv.Review(context.Background(), Request{ID: "r3", Kind: KindGoal, Subject: "goal-1", Summary: "目标"})
	if v.Approved || !strings.Contains(strings.Join(v.Reasons, ";"), "判据") {
		t.Fatalf("目标无判据应被拒: %v", v)
	}
	v2, _ := rv.Review(context.Background(), Request{ID: "r4", Kind: KindGoal, Subject: "goal-1", Summary: "目标", Criteria: []string{"有回执"}})
	if !v2.Approved {
		t.Fatalf("有判据应批准: %v", v2.Reasons)
	}
}

func TestRuleReviewerRejectsUnknownKindAndSubject(t *testing.T) {
	rv := NewRuleReviewer()
	v, _ := rv.Review(context.Background(), Request{ID: "r5", Kind: "bogus", Subject: "", Summary: "x"})
	if v.Approved {
		t.Fatal("未知类型 + 空对象应被拒")
	}
}

func TestHumanReviewer(t *testing.T) {
	h := &HumanReviewer{Ask: func(r Request) (bool, []string, error) {
		return false, []string{"我不信"}, nil
	}}
	v, err := h.Review(context.Background(), Request{ID: "h1", Kind: KindResult, Subject: "s", Summary: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Approved || !strings.Contains(strings.Join(v.Reasons, ";"), "我不信") {
		t.Fatalf("人工驳回应带理由: %v", v)
	}
	if h.Name() != "human" {
		t.Fatalf("默认名应为 human, got %s", h.Name())
	}
	// 未注入 Ask ⇒ 报错（fail-closed）
	bad := &HumanReviewer{}
	if _, err := bad.Review(context.Background(), Request{}); err == nil {
		t.Fatal("未注入 Ask 应报错")
	}
}

func TestQueueSubmitDecideAndLatest(t *testing.T) {
	q := newQueue(t)
	req, err := q.Submit(Request{Kind: KindGoal, Subject: "goal-A", Summary: "审一下", Criteria: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.ID == "" {
		t.Fatal("ID 应自动补")
	}
	if len(q.Pending()) != 1 {
		t.Fatal("应有一条待审")
	}
	if _, ok := q.LatestFor("goal-A"); ok {
		t.Fatal("未审前不应有结论")
	}
	// 驳回后 LatestFor 应返回未批准
	if err := q.Decide(Verdict{RequestID: req.ID, Approved: false, Reasons: []string{"不够"}, Reviewer: "rule"}); err != nil {
		t.Fatal(err)
	}
	v, ok := q.LatestFor("goal-A")
	if !ok || v.Approved {
		t.Fatalf("应拿到未批准结论: %+v ok=%v", v, ok)
	}
	if len(q.Pending()) != 0 {
		t.Fatal("审完应离开待审队列")
	}
	// 再审一次并批准
	req2, _ := q.Submit(Request{Kind: KindGoal, Subject: "goal-A", Summary: "再审", Criteria: []string{"c"}})
	if err := q.Decide(Verdict{RequestID: req2.ID, Approved: true, Reasons: []string{"过了"}, Reviewer: "human"}); err != nil {
		t.Fatal(err)
	}
	v2, _ := q.LatestFor("goal-A")
	if !v2.Approved {
		t.Fatal("最新结论应为批准")
	}
	// 审不存在的请求应报错
	if err := q.Decide(Verdict{RequestID: "nope"}); err == nil {
		t.Fatal("审不存在请求应报错")
	}
}

func TestQueuePersistenceAndRunAndRecord(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	req, _ := q.Submit(Request{Kind: KindPlan, Subject: "s1", Summary: "x", Evidence: []string{"e"}})
	v, err := q.RunAndRecord(context.Background(), NewRuleReviewer(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Approved {
		t.Fatalf("完整请求经规则二审应批准: %v", v.Reasons)
	}
	q2, _ := NewQueue(dir)
	if len(q2.Pending()) != 0 {
		t.Fatal("重开后待审应为空")
	}
	if got := q2.History(); len(got) != 1 || !got[0].Approved {
		t.Fatalf("重开应读到结论: %+v", got)
	}
}

func TestApprovedGate(t *testing.T) {
	q := newQueue(t)
	gate := ApprovedGate(q)

	// 无结论 ⇒ 不放行
	if ok, reason := gate("goal-X"); ok || !strings.Contains(reason, "fail-closed") {
		t.Fatalf("无二审结论应不放行: ok=%v reason=%q", ok, reason)
	}
	req, _ := q.Submit(Request{Kind: KindGoal, Subject: "goal-X", Summary: "s", Criteria: []string{"c"}})
	if err := q.Decide(Verdict{RequestID: req.ID, Approved: false, Reasons: []string{"弱"}, Reviewer: "rule"}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := gate("goal-X"); ok || !strings.Contains(reason, "弱") {
		t.Fatalf("未批准应不放行并带理由: ok=%v reason=%q", ok, reason)
	}
	req2, _ := q.Submit(Request{Kind: KindGoal, Subject: "goal-X", Summary: "s", Criteria: []string{"c"}})
	if err := q.Decide(Verdict{RequestID: req2.ID, Approved: true, Reviewer: "human"}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := gate("goal-X"); !ok || !strings.Contains(reason, "human") {
		t.Fatalf("批准应放行并留痕: ok=%v reason=%q", ok, reason)
	}
	// nil 队列 ⇒ 不放行
	if ok, _ := ApprovedGate(nil)("goal-X"); ok {
		t.Fatal("nil 队列应不放行")
	}
}

func TestSubmitValidates(t *testing.T) {
	q := newQueue(t)
	if _, err := q.Submit(Request{Kind: KindGoal, Summary: "x"}); err == nil {
		t.Fatal("空 subject 应被拒")
	}
	if _, err := q.Submit(Request{Subject: "s", Summary: "x"}); err == nil {
		t.Fatal("空 kind 应被拒")
	}
	if _, err := q.RunAndRecord(context.Background(), NewRuleReviewer(), "ghost"); err == nil {
		t.Fatal("审不存在请求应报错")
	}
}
