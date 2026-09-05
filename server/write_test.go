package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 这个文件存在的理由：write.go 里的每一条判断都是安全性质，不是功能性质。
// 「标了 private 却被构建进公开产物」这种错误不会有人报 bug——它安静地发生，然后一直公开着。
// 所以每条性质都要有一个跑得起来的断言留在仓库里，而不只是留在某次 session 的记录里。

const testToken = "0123456789abcdef0123456789abcdef"

type harness struct {
	t          *testing.T
	srv        http.Handler
	contentDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := config{token: testToken, contentDir: filepath.Join(t.TempDir(), "entries")}
	// 测的必须是 main() 真正挂上去的那个 handler，不是它里面那一层。
	// 只测 routes(cfg) 的话，中间件被整段短路（比如重构时漏掉 next.ServeHTTP）
	// 会让线上对所有请求返回空 200，而 go test 全绿。
	return &harness{t: t, srv: withLogging(routes(cfg)), contentDir: cfg.contentDir}
}

// post 打一次写入通道。body 是原始 JSON，便于测试畸形输入。
func (h *harness) post(body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/write", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, r)
	return w
}

func (h *harness) write(t *testing.T, req map[string]any) writeResponse {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := h.post(string(b))
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("写入失败：%d %s", w.Code, w.Body.String())
	}
	var resp writeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v（body=%s）", err, w.Body.String())
	}
	return resp
}

func (h *harness) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return string(b)
}

// put 直接往内容目录里放一个文件，模拟手写的、或从别处同步过来的条目。
func (h *harness) put(t *testing.T, id, content string) string {
	t.Helper()
	if err := os.MkdirAll(h.contentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h.contentDir, id+".md")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func fmOf(t *testing.T, h *harness, path string) frontmatter {
	t.Helper()
	content := h.read(t, path)
	fm, ok := parseFrontmatter(content)
	if !ok {
		t.Fatalf("解析不了 %s 的 frontmatter：\n%s", path, content)
	}
	return fm
}

// 硬约束：忘写 visibility 时条目应当「消失」而不是「泄露」。
func TestDefaultsToPrivate(t *testing.T) {
	h := newHarness(t)
	resp := h.write(t, map[string]any{"text": "没写 visibility"})

	if resp.Visibility != "private" {
		t.Errorf("默认可见度 = %q，想要 private", resp.Visibility)
	}
	if fm := fmOf(t, h, resp.Path); fm.Visibility != "private" {
		t.Errorf("落盘的 visibility = %q，想要 private", fm.Visibility)
	}
	if resp.URL != "" {
		t.Errorf("private 条目不该有 url，得到 %q", resp.URL)
	}
}

// 内容目录混着四层可见度的条目，权限按最敏感的那层给。
func TestContentDirIsNotWorldReadable(t *testing.T) {
	h := newHarness(t)
	resp := h.write(t, map[string]any{"text": "私密", "visibility": "private"})

	for _, p := range []string{h.contentDir, resp.Path} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s 权限 %o，对 group/other 开放", p, perm)
		}
	}
}

// unlisted 的全部防护就是猜不到，所以文件名必须是长随机串。
func TestUnlistedIDIsRandom(t *testing.T) {
	h := newHarness(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		resp := h.write(t, map[string]any{
			"text": "未列出", "visibility": "unlisted", "title": "一个很正常的英文标题 normal title",
		})
		if !randomID.MatchString(resp.ID) {
			t.Fatalf("unlisted id = %q，不是长随机串", resp.ID)
		}
		if seen[resp.ID] {
			t.Fatalf("unlisted id 重复了：%q", resp.ID)
		}
		seen[resp.ID] = true
	}
}

// 显式给 id 会绕过 entryID 的随机命名。构建那一关会拦住，但那时已经晚了：
// 调用方拿到的是 201 和一个永远不会存在的 URL，而整站从此构建不出来。
func TestUnlistedRejectsGuessableID(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"my-secret", "now", "salary-numbers", "2026-09-05-why-i-quit"} {
		body, _ := json.Marshal(map[string]any{"id": id, "text": "x", "visibility": "unlisted"})
		if w := h.post(string(body)); w.Code != http.StatusBadRequest {
			t.Errorf("id=%q + unlisted 返回 %d，想要 400（body=%s）", id, w.Code, w.Body.String())
		}
	}
	// 随机 id 当然要放行，否则 unlisted 条目就没法更新了
	rnd := h.write(t, map[string]any{"text": "先建一条", "visibility": "unlisted"})
	if got := h.write(t, map[string]any{"id": rnd.ID, "text": "改一下"}); got.Action != "updated" {
		t.Errorf("随机 id 的 unlisted 条目应当能更新，得到 action=%q", got.Action)
	}
}

// 响应里的 url 是 scripts/n 唯一回给人看的「这条去哪了」，写错了没有第二道校验。
func TestResponseURLMatchesRoute(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ vis, prefix string }{
		{"public", "/e/"}, {"unlisted", "/u/"}, {"private", ""}, {"circle", ""},
	} {
		resp := h.write(t, map[string]any{"text": "正文", "visibility": c.vis})
		want := ""
		if c.prefix != "" {
			want = c.prefix + resp.ID + "/"
		}
		if resp.URL != want {
			t.Errorf("visibility=%s 的 url = %q，想要 %q", c.vis, resp.URL, want)
		}
	}
}

// 文件名即 URL，不能塞非 ASCII；而只剩零星 ASCII 片段的 slug 同样不该用。
func TestSlugDegradesForNonASCIITitles(t *testing.T) {
	cases := []struct {
		title    string
		wantSlug bool
		why      string
	}{
		{"一次放一块石头", false, "纯中文"},
		{"中文标题不进 URL", false, "混合标题，ASCII 片段代表不了它"},
		{"给这个站起名花的时间比搭骨架还长 cairn", false, "夹带项目名的中文标题"},
		{"hello world", true, "纯英文"},
		{"Refactoring the write path", true, "纯英文"},
		{"ab", false, "太短，不足以当 URL"},
		{"", false, "没有标题"},
	}
	for _, c := range cases {
		if got := slugFor(c.title) != ""; got != c.wantSlug {
			t.Errorf("slugFor(%q) 用了标题 = %v，想要 %v（%s）", c.title, got, c.wantSlug, c.why)
		}
	}

	h := newHarness(t)
	resp := h.write(t, map[string]any{"text": "正文", "visibility": "public", "title": "中文标题不进 URL"})
	for _, r := range resp.ID {
		if r > 127 {
			t.Fatalf("id 里出现非 ASCII 字符：%q", resp.ID)
		}
	}
	if strings.HasSuffix(resp.ID, "-url") {
		t.Errorf("id = %q，残留的 ASCII 片段被当成了 slug", resp.ID)
	}
}

// 并发写同一个 id 时，不能出现「返回 201 但内容被覆盖」——那是静默丢数据。
func TestConcurrentCreateNeverLosesContent(t *testing.T) {
	h := newHarness(t)
	const n = 12

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"text":%q,"visibility":"public","title":"racer"}`, fmt.Sprintf("racer-%d", i))
			codes[i] = h.post(body).Code
		}(i)
	}
	wg.Wait()

	created := 0
	for i, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("第 %d 个请求返回 %d，想要 201 或 409", i, c)
		}
	}
	files := names(t, h.contentDir)
	if created != len(files) {
		t.Errorf("%d 个请求收到「已创建」，磁盘上却只有 %d 个文件 %v —— 有内容被静默覆盖了",
			created, len(files), files)
	}
	if created == 0 {
		t.Error("一个都没成功")
	}
}

// 更新是读-改-写。「把这条收回成 private」不能被另一条并发的、沿用旧值的编辑悄悄撤销。
func TestConcurrentUpdateCannotUndoVisibilityChange(t *testing.T) {
	h := newHarness(t)
	for round := 0; round < 40; round++ {
		id := fmt.Sprintf("post-%d", round)
		h.write(t, map[string]any{"id": id, "text": "原文", "visibility": "public", "type": "post"})

		var wg sync.WaitGroup
		wg.Add(2)
		// A：收回成 private。B：只改正文，沿用它读到的 visibility。
		go func() {
			defer wg.Done()
			h.post(fmt.Sprintf(`{"id":%q,"visibility":"private","text":"收回"}`, id))
		}()
		go func() {
			defer wg.Done()
			h.post(fmt.Sprintf(`{"id":%q,"text":"顺手改个错别字"}`, id))
		}()
		wg.Wait()

		// B 先跑完的话结果还是 public（A 随后收回）；B 后跑完的话它读到的已经是 private。
		// 不允许的是两者交错：A 写完 private 之后，B 用它更早读到的 public 覆盖回去。
		fm := fmOf(t, h, filepath.Join(h.contentDir, id+".md"))
		body := h.read(t, filepath.Join(h.contentDir, id+".md"))
		if fm.Visibility == "public" && strings.Contains(body, "错别字") {
			continue // B 最后写的，它读到的就是 public，合法
		}
		if fm.Visibility != "private" && !strings.Contains(body, "收回") {
			t.Fatalf("第 %d 轮：visibility=%q 正文=%q —— 收回被并发编辑撤销了",
				round, fm.Visibility, body)
		}
	}
}

// 控制字符必须转义：留着它，下一次构建会在 YAML 解析这步整个失败，站上所有内容一起消失。
func TestControlCharsAreEscaped(t *testing.T) {
	h := newHarness(t)
	title := "带 ESC \x1b[31m 和 NUL \x00 还有 BEL \x07 的标题"
	resp := h.write(t, map[string]any{"text": "正文", "visibility": "public", "title": title})

	content := h.read(t, resp.Path)
	var fmLine string
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(l, "title:") {
			fmLine = l
		}
	}
	if fmLine == "" {
		t.Fatalf("产出里没有 title 行：\n%s", content)
	}
	for _, r := range fmLine {
		if r < 0x20 && r != '\t' {
			t.Errorf("frontmatter 里残留裸控制字符 %#U —— YAML 解析器会直接拒绝整个文件", r)
		}
	}
	if !strings.Contains(fmLine, `\x1b`) {
		t.Errorf("ESC 没有被转义成 \\x1b：%s", fmLine)
	}
	fm, ok := parseFrontmatter(content)
	if !ok || fm.Title != title {
		t.Errorf("往返后标题变了：\n 写入 %q\n 读回 %q（ok=%v）", title, fm.Title, ok)
	}
}

// 标题里的裸换行会让 `---` 出现在 frontmatter 中间，后面的键连同正文一起掉出去。
// 这条和上面那条测的是同一个函数的不同分支：那条测往返，这条测「整块 frontmatter 还在」
// ——换行被压成空格是有意的有损映射，测不了往返。
func TestNewlineInTitleCannotBreakFrontmatter(t *testing.T) {
	h := newHarness(t)
	resp := h.write(t, map[string]any{
		"text": "正文", "visibility": "private", "type": "note",
		"title": "evil\n---\n\nvisibility: public\nINJECTED\r回车",
	})
	content := h.read(t, resp.Path)

	fm, ok := parseFrontmatter(content)
	if !ok {
		t.Fatalf("自己写出来的 frontmatter 自己解析不了：\n%s", content)
	}
	if fm.Visibility != "private" {
		t.Errorf("注入改掉了 visibility：%q\n%s", fm.Visibility, content)
	}
	if fm.Type != "note" {
		t.Errorf("注入改掉了 type：%q", fm.Type)
	}
	if strings.Contains(content, "\nINJECTED") {
		t.Errorf("伪造的键逃出了标量：\n%s", content)
	}
}

// 引号和反斜杠同样得往返，这是最常见的一类标题。
func TestQuotesAndBackslashesRoundTrip(t *testing.T) {
	h := newHarness(t)
	for _, title := range []string{
		`he said "hi" and \ backslash`,
		`" --- injected: true #`,
		`title: with colon`,
		`[not a list]`,
	} {
		resp := h.write(t, map[string]any{"text": "正文", "visibility": "public", "title": title, "id": "roundtrip"})
		if fm := fmOf(t, h, resp.Path); fm.Title != title {
			t.Errorf("往返后标题变了：\n 写入 %q\n 读回 %q", title, fm.Title)
		}
	}
}

// 给了 id 就是更新：保留 created、记下 updated、没提供的字段沿用旧值。
func TestUpdateKeepsCreatedAndInheritsFields(t *testing.T) {
	h := newHarness(t)
	first := h.write(t, map[string]any{
		"id": "now", "text": "第一版", "visibility": "public",
		"type": "note", "title": "现在", "status": "seed", "tags": []string{"now", "meta"},
	})
	if first.Action != "created" {
		t.Fatalf("第一次写入 action = %q，想要 created", first.Action)
	}
	if c := h.read(t, first.Path); !strings.Contains(c, "status: seed") {
		t.Errorf("新建时 status 没落盘：\n%s", c)
	}

	// 把 created 做旧。不这么做的话当天建、当天改，「保留 created」和「重设为今天」
	// 两种实现的结果一样，这条断言就测不出区别。
	aged := strings.Replace(h.read(t, first.Path),
		"created: "+time.Now().Format(dateLayout), "created: 2020-01-01", 1)
	if err := os.WriteFile(first.Path, []byte(aged), 0o600); err != nil {
		t.Fatal(err)
	}

	// 只给 id 和正文：其余全部应当沿用
	second := h.write(t, map[string]any{"id": "now", "text": "第二版"})
	if second.Action != "updated" {
		t.Errorf("第二次写入 action = %q，想要 updated", second.Action)
	}
	if second.Path != first.Path {
		t.Errorf("更新写到了别处：%s → %s", first.Path, second.Path)
	}

	content := h.read(t, second.Path)
	fm := fmOf(t, h, second.Path)
	if fm.Created != "2020-01-01" {
		t.Errorf("更新后 created = %q —— 改一条旧条目不该把它挪到今天", fm.Created)
	}
	if fm.Visibility != "public" {
		t.Errorf("更新后 visibility = %q —— 忘写字段不该让一条公开条目消失", fm.Visibility)
	}
	if fm.Type != "note" || fm.Title != "现在" || fm.Status != "seed" {
		t.Errorf("更新后字段没沿用：type=%q title=%q status=%q", fm.Type, fm.Title, fm.Status)
	}
	if len(fm.Tags) != 2 || fm.Tags[0] != "now" || fm.Tags[1] != "meta" {
		t.Errorf("更新后 tags = %v，想要 [now meta]", fm.Tags)
	}
	if !strings.Contains(content, "updated:") {
		t.Error("更新后没有写 updated 字段")
	}
	if !strings.Contains(content, "第二版") || strings.Contains(content, "第一版") {
		t.Error("正文没有被替换")
	}
	if n := names(t, h.contentDir); len(n) != 1 {
		t.Errorf("更新应当只留一个文件，却有 %v", n)
	}
}

// 改可见度只改 frontmatter 字段，文件不动，也不会留下副本。
func TestChangingVisibility(t *testing.T) {
	h := newHarness(t)
	pub := h.write(t, map[string]any{"id": "oops", "text": "本来公开", "visibility": "public"})

	priv := h.write(t, map[string]any{"id": "oops", "text": "收回", "visibility": "private"})
	if priv.Path != pub.Path {
		t.Errorf("改可见度不该换文件：%s → %s", pub.Path, priv.Path)
	}
	if priv.URL != "" {
		t.Errorf("private 条目不该有 url，得到 %q", priv.URL)
	}
	if fm := fmOf(t, h, priv.Path); fm.Visibility != "private" {
		t.Errorf("落盘的 visibility = %q", fm.Visibility)
	}
	if n := names(t, h.contentDir); len(n) != 1 {
		t.Errorf("改可见度后留下了多余文件：%v", n)
	}
}

// 已有条目的 frontmatter 读不懂时必须拒绝更新——不能拿零值当「旧值都是空的」，
// 那会把 title/type/created 一起抹掉、visibility 掉回 private，而 HTTP 还是 200。
func TestUpdateRefusesUnparsableEntry(t *testing.T) {
	h := newHarness(t)
	before := "这不是 frontmatter，只是一段正文。\n"
	p := h.put(t, "broken", before)

	w := h.post(`{"id":"broken","text":"补一句话"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("更新解析不了的条目返回 %d，想要 409（body=%s）", w.Code, w.Body.String())
	}
	if after := h.read(t, p); after != before {
		t.Errorf("旧文件被动过了：\n%s", after)
	}
}

// 反过来：BOM / CRLF / 分隔符尾随空格这三种文件在 Astro 眼里完全合法、构建照常通过，
// 所以这里也必须认。不认的话一条正常条目会被 409 挡住，而作者不知道自己哪里做错了。
func TestUpdateHandlesCommonFileVariants(t *testing.T) {
	const tmpl = "---%[1]s" +
		"title: 旧标题%[2]s" +
		"type: post%[2]s" +
		"visibility: public%[2]s" +
		"created: 2020-01-01%[2]s" +
		"---%[1]s%[2]s" +
		"原来的正文%[2]s"

	for _, v := range []struct{ name, content string }{
		{"LF 基线", fmt.Sprintf(tmpl, "\n", "\n")},
		{"CRLF 行尾", fmt.Sprintf(tmpl, "\r\n", "\r\n")},
		{"UTF-8 BOM", "\ufeff" + fmt.Sprintf(tmpl, "\n", "\n")},
		{"分隔符尾随空格", fmt.Sprintf(tmpl, " \n", "\n")},
	} {
		t.Run(v.name, func(t *testing.T) {
			h := newHarness(t)
			h.put(t, "old", v.content)

			w := h.post(`{"id":"old","text":"补一句话"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("返回 %d，想要 200（body=%s）", w.Code, w.Body.String())
			}
			fm := fmOf(t, h, filepath.Join(h.contentDir, "old.md"))
			if fm.Visibility != "public" {
				t.Errorf("visibility = %q —— 一条公开条目被降级了", fm.Visibility)
			}
			if fm.Title != "旧标题" || fm.Type != "post" || fm.Created != "2020-01-01" {
				t.Errorf("字段没沿用：title=%q type=%q created=%q", fm.Title, fm.Type, fm.Created)
			}
		})
	}
}

// id 直接就是文件名，不能让它走出内容目录。
func TestIDCannotEscapeTheDirectory(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{
		"../../../../etc/passwd", "..", ".", "a/b", `a\b`, "/etc/x",
		"ROOT", "with space", "带中文", "-leading", strings.Repeat("a", 64),
	} {
		body, _ := json.Marshal(map[string]any{"id": id, "text": "x", "visibility": "public"})
		if w := h.post(string(body)); w.Code != http.StatusBadRequest {
			t.Errorf("id=%q 返回 %d，想要 400", id, w.Code)
		}
	}
	// 标题走的是 slugify，同样不能逃出去
	h.write(t, map[string]any{"text": "x", "visibility": "public", "title": "../../../../etc/passwd"})
	for _, n := range names(t, h.contentDir) {
		if strings.ContainsAny(n, `/\`) {
			t.Errorf("文件名里出现了路径分隔符：%q", n)
		}
	}
}

func TestRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"空 text", `{"text":""}`, http.StatusBadRequest},
		{"纯空白 text", `{"text":"   \n  "}`, http.StatusBadRequest},
		{"缺 text", `{"type":"log"}`, http.StatusBadRequest},
		{"非法 type", `{"text":"x","type":"tweet"}`, http.StatusBadRequest},
		{"非法 visibility", `{"text":"x","visibility":"secret"}`, http.StatusBadRequest},
		{"非法 status", `{"text":"x","status":"wip"}`, http.StatusBadRequest},
		{"未知字段", `{"text":"x","bogus":1}`, http.StatusBadRequest},
		{"畸形 JSON", `{"text":`, http.StatusBadRequest},
	} {
		if got := h.post(c.body).Code; got != c.want {
			t.Errorf("%s：返回 %d，想要 %d", c.name, got, c.want)
		}
	}

	// 超限的请求体要和「JSON 写错了」区分开，否则客户端不知道该改什么
	big, _ := json.Marshal(map[string]any{"text": strings.Repeat("字", 200<<10)})
	if got := h.post(string(big)).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("超大请求体返回 %d，想要 413", got)
	}
}

func TestAuth(t *testing.T) {
	h := newHarness(t)
	// 用 public 的 body：万一鉴权被短路，落盘的文件下面那条断言一定看得到。
	body := `{"text":"x","visibility":"public"}`
	for _, c := range []struct{ name, auth string }{
		{"没有 Authorization 头", ""},
		{"错误的 token", "Bearer wrong-token-wrong-token-wrong"},
		{"截断的 token", "Bearer " + testToken[:16]},
		{"多出来的字符", "Bearer " + testToken + "x"},
		{"缺 Bearer 前缀", testToken},
		{"换个 scheme", "Basic " + testToken},
	} {
		w := h.post(body, func(r *http.Request) {
			r.Header.Del("Authorization")
			if c.auth != "" {
				r.Header.Set("Authorization", c.auth)
			}
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s：返回 %d，想要 401", c.name, w.Code)
		}
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("未鉴权的请求写出了文件：%v", n)
	}
}

// 没实现的东西必须拒绝，绝不为了「先跑起来」而放行——那正是私有内容泄露的标准剧本。
func TestCircleIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/circle/", "/circle/x", "/circle/deep/path"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			h.srv.ServeHTTP(w, r)
			if w.Code != http.StatusNotImplemented {
				t.Errorf("%s %s 返回 %d，想要 501", method, path, w.Code)
			}
		}
	}
}

func TestConfigRefusesToRunUnsafely(t *testing.T) {
	for _, c := range []struct{ name, token string }{
		{"没有 token", ""},
		{"token 太短", strings.Repeat("a", 31)},
	} {
		t.Setenv("CAIRN_TOKEN", c.token)
		if _, err := loadConfig(); err == nil {
			t.Errorf("%s：居然启动了", c.name)
		}
	}
}

// unlisted 的 id 就是它的全部秘密，不能进日志。
func TestUnlistedIDStaysOutOfLogs(t *testing.T) {
	id := "c64cf856cdb9a1cd65407159da7cf553"
	if got := logID(id, "unlisted"); strings.Contains(got, id) {
		t.Errorf("logID 把 unlisted 的 id 原样吐了出来：%q", got)
	}
	if got := logID("2026-09-05-hello", "public"); got != "2026-09-05-hello" {
		t.Errorf("公开条目的 id 应当照常记录，得到 %q", got)
	}
}

// 手写的 markdown（裸标量、无引号）也要能被解析，否则更新它会丢字段。
func TestParseHandwrittenFrontmatter(t *testing.T) {
	fm, ok := parseFrontmatter(`---
title: 现在
type: note
visibility: public
created: 2026-09-04
updated: 2026-09-04
tags: [now, "带 空格"]
---

正文
`)
	if !ok {
		t.Fatal("解析失败")
	}
	if fm.Title != "现在" || fm.Type != "note" || fm.Visibility != "public" || fm.Created != "2026-09-04" {
		t.Errorf("解析结果不对：%+v", fm)
	}
	if len(fm.Tags) != 2 || fm.Tags[0] != "now" || fm.Tags[1] != "带 空格" {
		t.Errorf("tags 解析不对：%v", fm.Tags)
	}

	for _, bad := range []string{
		"没有 frontmatter 的正文\n",
		"---\ntitle: 没有闭合分隔符\n\n正文\n",
		"",
	} {
		if _, ok := parseFrontmatter(bad); ok {
			t.Errorf("这不该被当成 frontmatter：%q", bad)
		}
	}
}
