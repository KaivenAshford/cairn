// 命令 ai：把 Claude Code 的 transcript 聚合成 /ai 那一页要用的 JSON。
//
//	cd pipeline && go run ./ai            # 采集并合并进 web/src/data/ai.json
//	go run ./ai -dry                      # 只打印摘要，不写盘
//	go run ./ai -src ~/.claude/projects -tz 8
//
// 为什么是 Go 而不是 Python 或 shell：
//
//   - **和 server/ 同一门语言、同一条零依赖约束。** 这个仓库已经有一套 Go 工具链和
//     24 个 Go 测试，采集器用 Go 就不用引入第二套。（scripts/n 里确实嵌了十几行
//     Python，但那是为了拼一个 JSON 字符串，不是一个要长期维护、要写断言的程序。）
//   - **隐私是这个程序的主要需求，而隐私需要测试。** 出口闸门必须能被单元测试
//     喂进一段假密钥、一条绝对路径，确认它真的拦。`go test` 是现成的。
//   - **几百 MB 的 jsonl 要流式读。** bufio.Scanner 逐行走，内存和文件数无关。
//
// 为什么单独一个 module（pipeline/go.mod）而不是并进 server/：
// 这两个程序没有任何共享代码，而且信任级别相反——server 是长期在线的服务，
// pipeline 是本机跑一次的批处理。放同一个 module 里，server 的依赖图就会平白多出
// 一个只有本机才跑的包。module 边界在这里就是「这两个东西不该互相 import」的强制版。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	src := flag.String("src", filepath.Join(home, ".claude", "projects"),
		"transcript 根目录（<项目>/<会话>.jsonl）")
	out := flag.String("out", "", "产出路径，默认是仓库里的 web/src/data/ai.json")
	tz := flag.Int("tz", 8, "本地时区相对 UTC 的小时偏移。日期和 24 小时分布都按它算")
	dry := flag.Bool("dry", false, "只打印摘要，不写盘")
	flag.Parse()

	if err := run(*src, *out, *tz, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "采集失败：", err)
		os.Exit(1)
	}
}

func run(src, out string, tz int, dry bool) error {
	if out == "" {
		root, err := repoRoot()
		if err != nil {
			return err
		}
		out = filepath.Join(root, "web", "src", "data", "ai.json")
	}

	sc := newScanner(tz)
	if err := sc.scanRoot(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 这不是错误，是常态：CI 和别的机器上根本没有 transcript。
			// 而合并规则保证「没看到 = 不改动」，所以这里继续往下走是安全的，
			// 走完只会把已有快照原样写回去。这条路径值得真的走一遍，
			// 它是「采集器不会在一台空机器上清空历史」这条性质的实际体现。
			fmt.Fprintf(os.Stderr, "没有 %s，这次一条 transcript 都没读到。\n", src)
		} else {
			return err
		}
	}

	fresh := sc.result(tz, time.Now().UTC().Add(time.Duration(tz)*time.Hour).Format("2006-01-02"))

	merged, err := load(out)
	if err != nil {
		return err
	}
	before := len(merged.Days)
	merged.merge(fresh)

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	// 闸门在写盘之前。拦下来就一个字节都不落地。
	if err := checkExport(data); err != nil {
		return fmt.Errorf("出口闸门拦住了这次产出，没有写盘：%w", err)
	}
	data = append(data, '\n')

	summary(merged, before, len(fresh.Days))
	if dry {
		fmt.Fprintln(os.Stderr, "（-dry，没有写盘）")
		return nil
	}
	return writeAtomic(out, data)
}

// repoRoot 从当前目录往上找同时有 web/ 和 pipeline/ 的那一层。
// 这样 `cd pipeline && go run ./ai` 和在仓库根上 `go run ./pipeline/ai` 都能跑，
// 不用记「必须在哪个目录下执行」——记不住的约定等于没有约定。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, "web")) && isDir(filepath.Join(dir, "pipeline")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("没找到仓库根目录（要有 web/ 和 pipeline/），请用 -out 指定产出路径")
		}
		dir = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// load 读已有快照。文件不在就返回一份空的——第一次跑不该报错。
// 但文件在却解不开必须报错：那多半是手改坏了，静默当成空的等于把历史一次性抹掉。
func load(path string) (*store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &store{Schema: schemaVersion, Days: map[string]*day{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("已有快照 %s 解不开，先修好它再跑（不会自动覆盖）：%w", path, err)
	}
	if s.Schema != schemaVersion {
		return nil, fmt.Errorf("已有快照的 schema 是 %d，这个版本是 %d。"+
			"字段含义变了，合并会算出假数——先决定旧快照是迁移还是丢弃", s.Schema, schemaVersion)
	}
	if s.Days == nil {
		s.Days = map[string]*day{}
	}
	return &s, nil
}

// writeAtomic：临时文件 + rename，和 server/write.go 同一套。
// 半截的 JSON 会让 astro build 直接失败，而这个命令很可能在 publish 之前跑。
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ai-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// summary 只打聚合数字。它和 JSON 受同一条约束：任何一行都可能被贴进 issue。
func summary(s *store, daysBefore, daysSeen int) {
	dates := s.dates()
	var prompts, calls, bash int
	for _, d := range s.Days {
		prompts += d.Prompts
		calls += d.ToolCalls
		bash += d.BashCmds
	}
	span := "（空）"
	if len(dates) > 0 {
		span = dates[0] + " → " + dates[len(dates)-1]
	}
	fmt.Fprintf(os.Stderr,
		"这次读到 %d 天，快照从 %d 天变成 %d 天。\n窗口 %s：%d 句话、%d 次工具调用、其中 %d 条 shell 命令。\n",
		daysSeen, daysBefore, len(s.Days), span, prompts, calls, bash)
}
