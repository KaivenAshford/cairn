package main

// 产出的数据结构，以及「历史快照怎么累积」这件事。
//
// 为什么按「天」切片而不是存一份总账：transcript 会被清理（Claude Code 自己会删旧的），
// 而这份 JSON 要进代码仓、要在没有 ~/.claude/projects 的机器上构建。
// 所以采集器必须能把**看到过的历史**留住，而不是每次全量重算——那样删掉的数据就永远丢了。
//
// 「天」是这里唯一正确的合并单位，因为它是**过去之后就不再变**的：
// 一个已经过完的日子不会再长出新事件（事件时间戳只会落在它发生的那天）。
// 于是合并规则可以极简：
//
//	同一天有两份观测 → 取事件数更多的那一份，**整份取**，不逐字段取 max。
//
// 逐字段取 max 会造出一份从来没存在过的日子：工具调用数来自 A、token 数来自 B，
// 两个数字互相对不上，页面上「平均每次调用多少 token」就是个假数。整份取则永远自洽。
//
// 这条规则同时给出两个性质，都在 stat_test.go 里钉住：
//   - 今天还在长 → 后一次观测事件更多 → 覆盖旧的（数据会更新）
//   - 旧 transcript 被删 → 后一次观测事件更少 → 保留旧的（历史不丢）
//
// 推论：在一台没有 transcript 的机器上跑一遍采集器，不会清空任何东西。
// 这不是巧合，是上面那条规则的直接后果，也是敢把它接进 CI 的前提。

import "sort"

// schemaVersion 变了就意味着旧快照的字段含义变了，合并会算出假数。
// 所以升版本时必须同时决定旧快照怎么办（迁移或丢弃），不能默默混在一起。
const schemaVersion = 1

// tokens 只记四个口径 + 思考。刻意不记 service_tier、iterations 那些：
// 采集的每一个字段都是一次「它会不会泄露」的判断，不用的就不采。
type tokens struct {
	In          int `json:"in"`          // 真正的新输入。缓存命中率高的时候它会小得离谱
	CacheRead   int `json:"cacheRead"`   // 重读缓存
	CacheCreate int `json:"cacheCreate"` // 写缓存
	Out         int `json:"out"`
	Thinking    int `json:"thinking"` // Out 的子集
}

// projects 是这份数据里最需要小心的一处：项目名由 cwd 派生（`-workspace-typescript-go-OH`
// 这种），里面有仓库名、有时还有客户名和分支名。
//
// 所以这里**一个名字都不出**，只出两个标量：当天碰过几个项目、其中最大的那个占多少。
// 这两个数字回答的问题（「是真的并行，还是每天只有一个主战场」）恰好不需要知道是哪个项目——
// 不需要的信息就不采，比采了再脱敏可靠：脱敏是一层可以写错的代码，不采没有代码可以写错。
type projects struct {
	Count       int `json:"count"`
	TopPermille int `json:"topPermille"` // 千分比整数。用整数是为了让出口闸门能一刀切「只准整数」
}

// day 是合并的原子单位。所有字段都是计数，没有比率——比率在页面上现算，
// 这样它永远和分子分母一致，而不是一个存下来之后就再也对不上的数。
type day struct {
	// Seen 是这一天被观测到的事件总数，只有一个用途：比较两份观测哪份更完整。
	// 它必须随每一条被计入的事件增长，否则合并规则会挑错那一份。
	Seen int `json:"seen"`

	Prompts     int   `json:"prompts"`     // 人说的话（不含工具回执、不含 slash 命令的回显）
	PromptChars []int `json:"promptChars"` // 每句话多长，分桶
	Turns       int   `json:"turns"`       // 助手事件数
	ToolCalls   int   `json:"toolCalls"`
	ToolErrors  int   `json:"toolErrors"` // 回执里 is_error 的那些

	Tools map[string]int `json:"tools"` // 键只可能是 toolClasses 里的那几个，见 gate.go

	BashCmds  int   `json:"bashCmds"`
	BashPiped int   `json:"bashPiped"` // 命令里出现过 | 的条数
	BashLen   []int `json:"bashLen"`   // 命令长度分桶。只存长度，命令原文一个字节都不留

	// Leverage 是「每一格里有多少句话」，LeverageCalls 是「这一格的话一共换来多少次调用」。
	// 两个都要：只有前者的话，页面只能拿桶的中点去估「少数几句话吃掉了多少动作」，
	// 而那个估计会随桶宽变——一个会随实现细节漂的数字不配当结论。
	Leverage      []int `json:"leverage"`
	LeverageCalls []int `json:"leverageCalls"`

	Hours []int `json:"hours"` // 24 格，本地时区。计的是工具调用

	Tokens   tokens   `json:"tokens"`
	Projects projects `json:"projects"`
}

func newDay() *day {
	return &day{
		PromptChars:   make([]int, len(promptCharBounds)+1),
		BashLen:       make([]int, len(bashLenBounds)+1),
		Leverage:      make([]int, len(leverageBounds)+1),
		LeverageCalls: make([]int, len(leverageBounds)+1),
		Hours:         make([]int, 24),
		Tools:         map[string]int{},
	}
}

type store struct {
	Schema int `json:"schema"`
	// Updated 是「这份快照最后一次被写」的日期，不是数据的截止日期——
	// 后者页面自己从 Days 的键里取，那才是真的。
	Updated string `json:"updated"`
	// 页面要用它给 24 小时那条轴写标签。不存的话，读者看到的「凌晨 4 点」是 UTC 的凌晨。
	TZOffsetHours int `json:"tzOffsetHours"`

	Days map[string]*day `json:"days"`
}

// merge 把一次新观测并进已有快照。见文件头那段：同一天整份取更完整的那一份。
func (s *store) merge(fresh *store) {
	if s.Days == nil {
		s.Days = map[string]*day{}
	}
	for date, nd := range fresh.Days {
		old, had := s.Days[date]
		if had && old.Seen >= nd.Seen {
			// 新观测没有更完整——这一天的 transcript 大概被清掉了。留住旧的。
			// 用 >= 而不是 >：两份一样完整时保持不动，让重复运行是幂等的
			//（不是省一次赋值，是让「跑两遍结果不变」成为一条能断言的性质）。
			continue
		}
		s.Days[date] = nd
	}
	s.Schema = schemaVersion
	s.TZOffsetHours = fresh.TZOffsetHours
	s.Updated = fresh.Updated
}

// dates 返回排好序的日期，给写文件和测试用（map 的迭代顺序是随机的）。
func (s *store) dates() []string {
	out := make([]string, 0, len(s.Days))
	for d := range s.Days {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// —— 分桶 ——
//
// 边界写死成常量而不是按数据分位数现算：分位数会随数据漂，昨天的「中位数桶」
// 和今天的不是同一个桶，历史快照就没法叠加了。合并的前提是桶的含义永不改变。

var (
	promptCharBounds = []int{10, 30, 80, 200}    // 一句话有多少字符
	bashLenBounds    = []int{80, 200, 500, 1500} // 一条 shell 命令有多长
	leverageBounds   = []int{1, 3, 6, 11, 31}    // 一句话换来多少次工具调用
)

// bucket 返回 v 落在哪一格：小于 bounds[0] 是 0，之后每越过一个边界加一。
func bucket(v int, bounds []int) int {
	i := 0
	for i < len(bounds) && v >= bounds[i] {
		i++
	}
	return i
}
