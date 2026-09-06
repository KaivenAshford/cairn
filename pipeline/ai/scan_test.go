package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 下面这些 fixture 里故意混进了「真实 transcript 会有、而产出里绝不能有」的东西：
// 绝对路径、一段像密钥的字符串、一句真话。断言的核心不是「数对了」，
// 是「数对了，而且原文一个字节都没跟着出来」。
const (
	secretCmd  = "grep -r AKIAIOSFODNN7EXAMPLE /home/zhang/私密项目/deploy/.env | head -5"
	secretTalk = "把 /home/zhang/私密项目 里的 AKIAIOSFODNN7EXAMPLE 换掉"
)

type line map[string]any

func writeJSONL(t *testing.T, path string, lines []line) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func userText(ts, text string, extra ...string) line {
	l := line{"type": "user", "timestamp": ts, "message": map[string]any{"content": text}}
	for _, k := range extra {
		l[k] = true
	}
	return l
}

func toolUse(ts string, uses ...map[string]any) line {
	blocks := make([]any, 0, len(uses))
	for _, u := range uses {
		b := map[string]any{"type": "tool_use", "name": u["name"]}
		if in, ok := u["input"]; ok {
			b["input"] = in
		}
		blocks = append(blocks, b)
	}
	return line{"type": "assistant", "timestamp": ts, "message": map[string]any{"content": blocks}}
}

func toolResult(ts string, isError bool) line {
	return line{"type": "user", "timestamp": ts, "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_result", "is_error": isError, "content": secretCmd}},
	}}
}

// fixture 是一份手写的小 transcript，覆盖了真实数据里每一种会影响计数的形状。
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// 项目 A：一句人话 → 4 次工具调用。tz=8 时全部落在 2026-01-01 本地 10 点。
	writeJSONL(t, filepath.Join(root, "-home-zhang-私密项目", "s1.jsonl"), []line{
		userText("2026-01-01T02:00:00.000Z", secretTalk),
		toolUse("2026-01-01T02:00:01.000Z", map[string]any{"name": "Bash", "input": map[string]any{"command": secretCmd}}),
		toolResult("2026-01-01T02:00:02.000Z", true),
		toolUse("2026-01-01T02:00:03.000Z",
			map[string]any{"name": "Read", "input": map[string]any{"file_path": "/home/zhang/私密项目/a.go"}},
			map[string]any{"name": "mcp__gitcode__create_pull_request_comment", "input": map[string]any{}},
		),
		toolUse("2026-01-01T02:00:04.000Z", map[string]any{"name": "没见过的工具", "input": map[string]any{}}),

		// 下面四条都不是人话，一条都不能算进 prompts。
		userText("2026-01-01T02:10:00.000Z", "<command-name>/compact</command-name>"),
		userText("2026-01-01T02:11:00.000Z", "<local-command-stdout>压缩完了</local-command-stdout>"),
		userText("2026-01-01T02:12:00.000Z", "环境注入的一段上下文", "isMeta"),
		userText("2026-01-01T02:13:00.000Z", "This session is being continued…", "isCompactSummary"),

		// token 记账。thinking 是 output 的子集。
		line{"type": "assistant", "timestamp": "2026-01-01T02:20:00.000Z", "message": map[string]any{
			"content": []any{map[string]any{"type": "thinking", "thinking": secretTalk, "signature": "AAAA"}},
			"usage": map[string]any{
				"input_tokens": 3, "output_tokens": 1000,
				"cache_read_input_tokens": 90000, "cache_creation_input_tokens": 5000,
				"output_tokens_details": map[string]any{"thinking_tokens": 400},
			},
		}},

		// 跨午夜的一轮：人话在本地 23:59，动作落在第二天 00:30。
		userText("2026-01-01T15:59:00.000Z", "继续"),
		toolUse("2026-01-01T16:30:00.000Z", map[string]any{"name": "Bash", "input": map[string]any{"command": "ls"}}),
	})

	// 项目 B：同一天的另一个项目，只有 1 次调用。用来算「当天最大的项目占几成」。
	writeJSONL(t, filepath.Join(root, "-workspace-另一个仓", "s2.jsonl"), []line{
		userText("2026-01-01T03:00:00.000Z", "看一下"),
		toolUse("2026-01-01T03:00:01.000Z", map[string]any{"name": "Write", "input": map[string]any{"content": secretTalk}}),
	})

	return root
}

func scanFixture(t *testing.T) *store {
	t.Helper()
	sc := newScanner(8)
	if err := sc.scanRoot(fixture(t)); err != nil {
		t.Fatal(err)
	}
	return sc.result(8, "2026-01-02")
}

func TestScanCounts(t *testing.T) {
	s := scanFixture(t)
	d1 := s.Days["2026-01-01"]
	if d1 == nil {
		t.Fatal("2026-01-01 没有记录")
	}

	// 人话只有三句：secretTalk、「继续」、「看一下」。
	// 机器注入的四条（<…> 两条、isMeta、isCompactSummary）一条都不算。
	if d1.Prompts != 3 {
		t.Errorf("人话 = %d，想要 3（机器注入的四条不该算进来）", d1.Prompts)
	}
	// 6 次调用里有 1 次落在 2 号（跨午夜那一轮），所以 1 号是 5 次。
	if got, want := d1.ToolCalls, 5; got != want {
		t.Errorf("工具调用 = %d，想要 %d", got, want)
	}
	if d1.ToolErrors != 1 {
		t.Errorf("失败回执 = %d，想要 1", d1.ToolErrors)
	}
	want := map[string]int{clsRun: 1, clsRead: 1, clsMCP: 1, clsOthers: 1, clsEdit: 1}
	for k, v := range want {
		if d1.Tools[k] != v {
			t.Errorf("tools[%s] = %d，想要 %d（全部：%v）", k, d1.Tools[k], v, d1.Tools)
		}
	}
	if len(d1.Tools) != len(want) {
		t.Errorf("tools 出现了预期外的键：%v", d1.Tools)
	}

	// mcp__gitcode__… 必须塌进「外部服务」，不能以原名出现——服务名等于报出雇主。
	if _, ok := d1.Tools["mcp__gitcode__create_pull_request_comment"]; ok {
		t.Error("MCP 工具名原样进了产出")
	}

	if d1.Tokens.In != 3 || d1.Tokens.Out != 1000 || d1.Tokens.CacheRead != 90000 ||
		d1.Tokens.CacheCreate != 5000 || d1.Tokens.Thinking != 400 {
		t.Errorf("token 记账不对：%+v", d1.Tokens)
	}
}

func TestScanBashOnlyKeepsShape(t *testing.T) {
	s := scanFixture(t)
	d1 := s.Days["2026-01-01"]
	if d1.BashCmds != 1 {
		t.Fatalf("bash 条数 = %d，想要 1", d1.BashCmds)
	}
	if d1.BashPiped != 1 {
		t.Errorf("带管道的 bash = %d，想要 1", d1.BashPiped)
	}
	// secretCmd 有 68 个字符，落在 [10,80) 那一格之后的第 1 格（bounds 80/200/500/1500）。
	if d1.BashLen[0] != 1 {
		t.Errorf("命令长度分桶 = %v，68 字符应该落在第 0 格", d1.BashLen)
	}
}

func TestScanLeverageBelongsToThePromptsDay(t *testing.T) {
	s := scanFixture(t)
	d1, d2 := s.Days["2026-01-01"], s.Days["2026-01-02"]

	// 三轮都记在 1 号（三句人话都在 1 号说的），哪怕最后一轮的动作发生在 2 号。
	if got := sum(d1.Leverage); got != 3 {
		t.Errorf("1 号的轮数 = %d，想要 3", got)
	}
	if d2 != nil && sum(d2.Leverage) != 0 {
		t.Errorf("2 号不该有轮数（那一轮的人话在 1 号说的）：%v", d2.Leverage)
	}
	// 但动作本身记在它发生的那天。
	if d2 == nil || d2.ToolCalls != 1 {
		t.Errorf("2 号的工具调用应该是 1（跨午夜的那一次）")
	}
	// 5 次调用的那一轮落在 [3,6) 那格；1 次的两轮落在 [1,3) 那格。
	if d1.Leverage[bucket(5, leverageBounds)] != 1 || d1.Leverage[bucket(1, leverageBounds)] != 2 {
		t.Errorf("杠杆分桶不对：%v", d1.Leverage)
	}
}

func TestScanTimezoneShiftsTheDay(t *testing.T) {
	root := fixture(t)
	// 同一份数据，tz=0 时 02:00Z 还是 1 号的凌晨 2 点；tz=8 时是 1 号上午 10 点。
	sc := newScanner(0)
	if err := sc.scanRoot(root); err != nil {
		t.Fatal(err)
	}
	utc := sc.result(0, "2026-01-02")
	if utc.Days["2026-01-01"].Hours[2] == 0 {
		t.Errorf("tz=0 时第一批调用应该落在 2 点：%v", utc.Days["2026-01-01"].Hours)
	}
	local := scanFixture(t)
	if local.Days["2026-01-01"].Hours[10] == 0 {
		t.Errorf("tz=8 时第一批调用应该落在 10 点：%v", local.Days["2026-01-01"].Hours)
	}
	// 跨午夜那次：tz=0 下 16:30Z 还在 1 号，tz=8 下已经是 2 号。
	if _, ok := utc.Days["2026-01-02"]; ok {
		t.Error("tz=0 时不该有 2026-01-02")
	}
	if _, ok := local.Days["2026-01-02"]; !ok {
		t.Error("tz=8 时应该有 2026-01-02")
	}
}

func TestScanProjectsAreCountedNotNamed(t *testing.T) {
	s := scanFixture(t)
	d1 := s.Days["2026-01-01"]
	if d1.Projects.Count != 2 {
		t.Errorf("项目数 = %d，想要 2", d1.Projects.Count)
	}
	// 1 号共 5 次调用，项目 A 占 4 次 → 800‰。
	if d1.Projects.TopPermille != 800 {
		t.Errorf("最大项目占比 = %d‰，想要 800‰", d1.Projects.TopPermille)
	}
}

// —— 这一条是整个 pipeline 里最要紧的断言 ——
//
// fixture 里塞了绝对路径、一段像 AWS key 的字符串、一句真实的中文对话，
// 它们都真的被采集器读到过（命令长度、管道、字数都是从它们身上量出来的）。
// 断言：序列化之后，这些原文一个字节都不在。
func TestExportCarriesNoRawText(t *testing.T) {
	s := scanFixture(t)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, needle := range []string{
		secretCmd, secretTalk,
		"AKIAIOSFODNN7EXAMPLE",        // 像密钥的那一段
		"/home/zhang", "私密项目", "另一个仓", // 路径与项目名
		"gitcode", "mcp__", // 接了哪些外部服务
		"没见过的工具",    // 未知工具的原名
		"继续", "看一下", // 人说的话
		".env", "a.go", // 文件名
	} {
		if strings.Contains(text, needle) {
			t.Errorf("产出里出现了原文 %q", needle)
		}
	}
	// 反过来钉一下：上面那串 needle 不是无的放矢，它们确实在输入里。
	if !strings.Contains(secretCmd, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("fixture 自己就没有那段假密钥，上面的断言是空转的")
	}

	// 出口闸门也得放行——它拦不住的东西才需要上面那串 needle，
	// 但它拦得住的东西不该在这里被写出来。
	indented, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkExport(indented); err != nil {
		t.Errorf("闸门拦下了正常产出：%v", err)
	}
}

func TestScanMissingRootIsAnError(t *testing.T) {
	sc := newScanner(8)
	if err := sc.scanRoot(filepath.Join(t.TempDir(), "根本不存在")); err == nil {
		t.Fatal("目录不存在时 scanRoot 应该报错（由 main 决定这是不是致命的）")
	}
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}
