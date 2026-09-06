package main

import (
	"encoding/json"
	"testing"
)

func obs(date string, seen, calls int) *store {
	d := newDay()
	d.Seen = seen
	d.ToolCalls = calls
	return &store{Schema: schemaVersion, Updated: "2026-01-09", TZOffsetHours: 8,
		Days: map[string]*day{date: d}}
}

// 今天还在长：后一次观测看到更多事件，应该覆盖。
func TestMergeGrowingDayIsUpdated(t *testing.T) {
	s := obs("2026-01-01", 10, 3)
	s.merge(obs("2026-01-01", 40, 12))
	if got := s.Days["2026-01-01"].ToolCalls; got != 12 {
		t.Errorf("工具调用 = %d，想要 12（更完整的那份观测该赢）", got)
	}
}

// —— 这一条是「增量」两个字的全部含义 ——
// 旧 transcript 被 Claude Code 清掉之后，再采一次只会看到更少的事件。
// 那一天的历史必须留在快照里，否则「删掉的数据永远丢了」。
func TestMergeShrunkDayKeepsHistory(t *testing.T) {
	s := obs("2026-01-01", 40, 12)
	s.merge(obs("2026-01-01", 10, 3)) // transcript 被清理过，只剩一小截
	if got := s.Days["2026-01-01"].ToolCalls; got != 12 {
		t.Errorf("工具调用 = %d，想要 12（transcript 被清理不该让历史缩水）", got)
	}
}

// 在一台没有 transcript 的机器上跑一遍，不能把快照清空。
// 这是敢把采集器接进任何自动流程的前提。
func TestMergeEmptyScanKeepsEverything(t *testing.T) {
	s := obs("2026-01-01", 40, 12)
	s.merge(&store{Schema: schemaVersion, Updated: "2026-02-01", TZOffsetHours: 8,
		Days: map[string]*day{}})
	if len(s.Days) != 1 || s.Days["2026-01-01"].ToolCalls != 12 {
		t.Fatalf("空扫描把快照清空了：%v", s.Days)
	}
}

func TestMergeAddsNewDays(t *testing.T) {
	s := obs("2026-01-01", 40, 12)
	s.merge(obs("2026-01-02", 5, 2))
	if len(s.Days) != 2 {
		t.Fatalf("新的一天没有并进来：%v", s.dates())
	}
}

// 跑两遍结果一样。不是为了省一次赋值：不幂等的话，
// 「今天已经采过了吗」就得靠人记，而这正是 CLAUDE.md 说的「靠记得」。
func TestMergeIsIdempotent(t *testing.T) {
	s := obs("2026-01-01", 40, 12)
	first, _ := json.Marshal(s)
	s.merge(obs("2026-01-01", 40, 12))
	second, _ := json.Marshal(s)
	if string(first) != string(second) {
		t.Errorf("同一份观测合并两次结果变了：\n%s\n%s", first, second)
	}
}

// 分桶边界。桶的含义一旦漂移，历史快照就没法叠加了——所以边界值本身要钉住。
func TestBucketBoundaries(t *testing.T) {
	cases := []struct {
		v    int
		want int
	}{
		{0, 0}, {1, 1}, {2, 1}, {3, 2}, {5, 2}, {6, 3}, {10, 3},
		{11, 4}, {30, 4}, {31, 5}, {999, 5},
	}
	for _, c := range cases {
		if got := bucket(c.v, leverageBounds); got != c.want {
			t.Errorf("bucket(%d) = %d，想要 %d", c.v, got, c.want)
		}
	}
	if n := len(newDay().Leverage); n != len(leverageBounds)+1 {
		t.Errorf("杠杆有 %d 格，边界有 %d 个，对不上", n, len(leverageBounds))
	}
	if got := bucket(79, bashLenBounds); got != 0 {
		t.Errorf("79 字符应该落在第 0 格，得到 %d", got)
	}
	if got := bucket(80, bashLenBounds); got != 1 {
		t.Errorf("80 字符应该落在第 1 格，得到 %d", got)
	}
}
