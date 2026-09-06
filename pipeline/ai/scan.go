package main

// 扫 transcript。
//
// 这个文件是整条链路上唯一碰得到原文的地方，所以它的规矩是：
// **原文只能是局部变量，绝不进任何结构体。** 命令原文只用来量长度和数管道，
// 人说的话只用来量字数和判断「这是不是一句人话」，量完就出作用域。
// 下游（stat.go / gate.go）因此连「泄露」这个可能性都没有——它们手上只有整数。
//
// 解码也是按这条规矩写的：event 里只声明用得上的字段。json 包会忽略其余全部内容，
// 于是 message.content 里的正文、tool_result 里的命令输出、thinking 的签名，
// 从来没有被解出来过。这比「解出来再删掉」强：没解出来就不会被谁顺手用上。

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 单行可以到几 MB（一次 Bash 的输出全在里面），默认 64 KiB 的 Scanner 会直接报错退出。
// 给 32 MiB 上限：再大的行只可能是异常数据，宁可跳过也不要把内存吃光。
const maxLineBytes = 32 << 20

type event struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`

	// 这两个标记是 Claude Code 自己塞进 user 事件里的：
	// isMeta 是环境注入的上下文，isCompactSummary 是压缩上下文时机器写的摘要。
	// 两者都长得像「用户说的话」，不排掉的话人类发言数会虚高三成。
	IsMeta           bool `json:"isMeta"`
	IsCompactSummary bool `json:"isCompactSummary"`

	Message struct {
		Content json.RawMessage `json:"content"`
		Usage   *usage          `json:"usage"`
	} `json:"message"`
}

type usage struct {
	In          int `json:"input_tokens"`
	Out         int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
	Details     struct {
		Thinking int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type block struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	IsError bool            `json:"is_error"`
	Input   json.RawMessage `json:"input"`
}

// bashInput 单独解：input 的形状随工具而变（Edit 里是 old_string/new_string），
// 声明成结构体会在别的工具上解码失败。只有确认是 Bash 时才往下解这一层。
type bashInput struct {
	Command string `json:"command"`
}

// —— 工具分类 ——
//
// 不出原始工具名，出**类别**。两个理由，第二个才是主要的：
//  1. mcp__ 开头的工具名会说出你接了哪些外部服务（`mcp__gitcode__…` 等于报出雇主）。
//  2. 更重要的是，要回答的问题本来就是类别层面的：
//     「它是靠跑命令干活，还是靠读写文件干活」。出一张 36 根柱子的原始工具名图
//     谁也看不出这件事，那就成了「今年 commit 1234 次」。
//
// 类别名是一个**封闭集合**（见 toolClasses），出口闸门只认这几个键。
// 于是「哪天多了一个没见过的工具」不会变成 JSON 里多一个键，只会落进「其他」。
const (
	clsRun    = "执行"
	clsRead   = "读文件"
	clsEdit   = "改文件"
	clsAgent  = "派活"
	clsWeb    = "外网"
	clsMCP    = "外部服务"
	clsAsk    = "问我"
	clsOthers = "其他"
)

// toolClasses 是 JSON 里 tools 这个 map 允许出现的**全部**键。gate.go 拿它做闸门。
var toolClasses = []string{clsRun, clsRead, clsEdit, clsAgent, clsWeb, clsMCP, clsAsk, clsOthers}

var toolClass = map[string]string{
	"Bash": clsRun, "BashOutput": clsRun, "KillShell": clsRun, "KillBash": clsRun,

	"Read": clsRead, "Glob": clsRead, "Grep": clsRead, "NotebookRead": clsRead,

	"Edit": clsEdit, "Write": clsEdit, "MultiEdit": clsEdit, "NotebookEdit": clsEdit,

	"Task": clsAgent, "TaskCreate": clsAgent, "TaskOutput": clsAgent,
	"TaskUpdate": clsAgent, "TaskStop": clsAgent, "Workflow": clsAgent,
	"Skill": clsAgent, "ListAgents": clsAgent, "SendMessage": clsAgent, "Monitor": clsAgent,

	"WebFetch": clsWeb, "WebSearch": clsWeb,

	"AskUserQuestion": clsAsk, "SendUserFile": clsAsk,
}

func classOf(name string) string {
	if c, ok := toolClass[name]; ok {
		return c
	}
	// MCP 工具名的形状是 mcp__<服务>__<方法>，服务名不能出去。
	if strings.HasPrefix(name, "mcp__") {
		return clsMCP
	}
	return clsOthers
}

// —— 「这是不是一句人话」——
//
// transcript 里的 user 事件绝大多数不是人说的：工具回执、环境注入、slash 命令的回显、
// 后台任务通知，全都是 type=user。这些是机器写给机器看的。
//
// 判据要**宁可少算不可多算**：这个数字是全站杠杆比的分母，分母虚高会让结论偏保守（安全），
// 分母虚低会让结论虚高（不安全）。所以规则是白名单式的——
// 只有「content 是个裸字符串、没有机器标记、不以 < 开头」才算人话。
// 以 < 开头一律不算：机器注入的东西全部是 <command-name> / <task-notification> 这种
// 标签形状，而人真的以 < 开头说话是极少数。少算几句，比把一堆机器回显算成人话强。
var machinePrefix = "<"

func isHumanPrompt(e *event, text string) bool {
	if e.IsMeta || e.IsCompactSummary {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(text), machinePrefix)
}

type scanner struct {
	tzOffset time.Duration
	days     map[string]*day
	// 每天每个项目的工具调用数。项目名只活在这张表里，reduce 之后就丢掉——
	// 它一个字节都不会进 store。
	byProject map[string]map[string]int
}

func newScanner(tzOffsetHours int) *scanner {
	return &scanner{
		tzOffset:  time.Duration(tzOffsetHours) * time.Hour,
		days:      map[string]*day{},
		byProject: map[string]map[string]int{},
	}
}

func (s *scanner) day(date string) *day {
	d, ok := s.days[date]
	if !ok {
		d = newDay()
		s.days[date] = d
	}
	return d
}

// scanRoot 扫 <root>/<项目>/<会话>.jsonl。
// 目录名就是项目身份（Claude Code 用 cwd 派生），但它只被当作一个不透明的分组键用。
func (s *scanner) scanRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, project := range names {
		files, err := filepath.Glob(filepath.Join(root, project, "*.jsonl"))
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, f := range files {
			if err := s.scanFile(project, f); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *scanner) scanFile(project, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	// 一「轮」= 一句人话，加上它引发的全部动作，直到下一句人话。
	// 这是杠杆比的分母。轮的归属日期取**人说话那一刻**，不取动作发生的时刻：
	// 跨过午夜的那一轮该记在提问的那天，不然「零点那句话换来 80 次调用」会变成
	// 「零点那天凭空多了 80 次调用却没人提问」。
	turnDate := ""
	turnTools := 0
	flush := func() {
		if turnDate == "" {
			return
		}
		d := s.day(turnDate)
		b := bucket(turnTools, leverageBounds)
		d.Leverage[b]++
		d.LeverageCalls[b] += turnTools
		turnDate, turnTools = "", 0
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			// 半行、被截断的文件都可能出现。跳过一行比让整次采集失败强。
			continue
		}
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		local := ts.UTC().Add(s.tzOffset)
		date := local.Format("2006-01-02")
		d := s.day(date)
		d.Seen++

		switch e.Type {
		case "user":
			s.userEvent(&e, d, date, &turnDate, &turnTools, flush)
		case "assistant":
			s.assistantEvent(&e, d, project, date, local.Hour(), &turnTools)
		}
	}
	flush()

	if err := sc.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (s *scanner) userEvent(e *event, d *day, date string, turnDate *string, turnTools *int, flush func()) {
	raw := e.Message.Content
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return
		}
		if !isHumanPrompt(e, text) {
			return
		}
		flush()
		*turnDate = date
		*turnTools = 0
		d.Prompts++
		// 只留字数。text 到这一行为止，下一句就出作用域了。
		d.PromptChars[bucket(len([]rune(text)), promptCharBounds)]++
		return
	}

	// 数组形态的 user 事件几乎全是工具回执。这里只关心它成没成功。
	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type == "tool_result" && b.IsError {
			d.ToolErrors++
		}
	}
}

func (s *scanner) assistantEvent(e *event, d *day, project, date string, hour int, turnTools *int) {
	d.Turns++
	if u := e.Message.Usage; u != nil {
		d.Tokens.In += u.In
		d.Tokens.Out += u.Out
		d.Tokens.CacheRead += u.CacheRead
		d.Tokens.CacheCreate += u.CacheCreate
		d.Tokens.Thinking += u.Details.Thinking
	}

	var blocks []block
	if len(e.Message.Content) == 0 || e.Message.Content[0] != '[' {
		return
	}
	if err := json.Unmarshal(e.Message.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		d.ToolCalls++
		d.Hours[hour]++
		d.Tools[classOf(b.Name)]++
		*turnTools++

		byDay, ok := s.byProject[date]
		if !ok {
			byDay = map[string]int{}
			s.byProject[date] = byDay
		}
		byDay[project]++

		if b.Name == "Bash" {
			s.bashCommand(d, b.Input)
		}
	}
}

// bashCommand 是整个采集器里唯一读命令原文的地方。它只做两件事：量长度、看有没有管道。
// 原文既不返回也不存，函数一结束就没了。
func (s *scanner) bashCommand(d *day, input json.RawMessage) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return
	}
	d.BashCmds++
	if strings.Contains(in.Command, "|") {
		d.BashPiped++
	}
	d.BashLen[bucket(len([]rune(strings.TrimSpace(in.Command))), bashLenBounds)]++
}

// result 把扫描结果收成一份快照。项目分布在这里被压成两个标量，名字随之丢弃。
func (s *scanner) result(tzOffsetHours int, today string) *store {
	for date, byProject := range s.byProject {
		d, ok := s.days[date]
		if !ok {
			continue
		}
		total, top := 0, 0
		for _, n := range byProject {
			total += n
			if n > top {
				top = n
			}
		}
		d.Projects.Count = len(byProject)
		if total > 0 {
			d.Projects.TopPermille = top * 1000 / total
		}
	}
	return &store{
		Schema:        schemaVersion,
		Updated:       today,
		TZOffsetHours: tzOffsetHours,
		Days:          s.days,
	}
}
