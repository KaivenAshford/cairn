package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 这条通道和 /api/write 的区别在于**它的地址是公网可达的，而且没有 Bearer 头保护**。
// 所以下面每一条都是安全性质，不是功能性质：secret token 比对、allowlist、
// 「陌生人不给任何回应」、「unlisted 的 id 不进日志」、「重投不会写出第二条」。
// 这些错了不会有人报 bug——它们安静地发生。

const (
	testTGSecret = "cairn-test-secret-0123456789abcdef"
	tgOwner      = int64(4242)
	tgStranger   = int64(9999)
)

type tgHarness struct {
	t          *testing.T
	srv        http.Handler
	contentDir string
	logs       *bytes.Buffer
}

func newTGHarness(t *testing.T) *tgHarness {
	t.Helper()
	cfg := config{
		token:      testToken,
		contentDir: filepath.Join(t.TempDir(), "entries"),
		siteURL:    "https://example.test",
		tg: telegramConfig{
			enabled: true,
			secret:  testTGSecret,
			allow:   map[int64]bool{tgOwner: true},
		},
	}
	// 日志要能断言：「unlisted 的 id 不进日志」和「陌生人的 id 进日志」都只能这么测。
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	// 和 write_test.go 一样，测的是 main() 真正挂上去的那个 handler：
	// 只测 handleTelegram 的话，路由注册写错（比如漏了 tg.enabled 判断）测不出来。
	return &tgHarness{t: t, srv: withLogging(routes(cfg)), contentDir: cfg.contentDir, logs: &logs}
}

func (h *tgHarness) post(body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/telegram", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(tgSecretHeader, testTGSecret)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.srv.ServeHTTP(w, r)
	return w
}

// tgJSON 拼一条 Telegram 的 update。多带几个我们不认识的字段是有意的：
// Bot API 每个版本都在加字段，收到它们必须照常工作。
func tgJSON(updateID, from int64, chatType, text string) string {
	msg := map[string]any{
		"message_id": 7,
		"date":       1757000000,
		"from": map[string]any{
			"id": from, "is_bot": false, "first_name": "Z", "language_code": "zh-hans",
		},
		"chat":              map[string]any{"id": from, "type": chatType, "first_name": "Z"},
		"has_media_spoiler": false,
	}
	if text != "" {
		msg["text"] = text
	}
	b, err := json.Marshal(map[string]any{"update_id": updateID, "message": msg})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (h *tgHarness) send(updateID int64, text string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.post(tgJSON(updateID, tgOwner, "private", text))
}

// replyOf 解出写在 webhook 响应体里的那次 sendMessage。
func (h *tgHarness) replyOf(w *httptest.ResponseRecorder) tgSendMessage {
	h.t.Helper()
	if w.Code != http.StatusOK {
		h.t.Fatalf("状态码 %d，想要 200（body=%s）", w.Code, w.Body.String())
	}
	var m tgSendMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		h.t.Fatalf("响应不是一次 sendMessage：%v（body=%q）", err, w.Body.String())
	}
	if m.Method != "sendMessage" {
		h.t.Fatalf("method = %q，想要 sendMessage", m.Method)
	}
	return m
}

// only 返回内容目录里唯一那个条目的路径，不是一个就直接失败。
func (h *tgHarness) only() string {
	h.t.Helper()
	n := names(h.t, h.contentDir)
	if len(n) != 1 {
		h.t.Fatalf("内容目录里有 %d 个文件 %v，想要 1 个", len(n), n)
	}
	return filepath.Join(h.contentDir, n[0])
}

func (h *tgHarness) entry(path string) (frontmatter, string) {
	h.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	fm, ok := parseFrontmatter(string(b))
	if !ok {
		h.t.Fatalf("解析不了 %s：\n%s", path, b)
	}
	_, body, _ := strings.Cut(strings.TrimPrefix(string(b), "---\n"), "\n---\n")
	return fm, strings.TrimSpace(body)
}

// ── 第一道门：secret token ──────────────────────────────────────────────────

// webhook 地址是公网可达的，这个头是「这个请求来自 Telegram」的全部证据。
func TestTelegramSecretToken(t *testing.T) {
	for _, c := range []struct{ name, secret string }{
		{"没有这个头", ""},
		{"完全不对", "wrong-secret-wrong-secret-wrong-x"},
		{"截断的", testTGSecret[:16]},
		{"多一个字符", testTGSecret + "x"},
		{"大小写不同", strings.ToUpper(testTGSecret)},
		{"拿 CAIRN_TOKEN 来试", testToken},
	} {
		h := newTGHarness(t)
		w := h.post(tgJSON(1, tgOwner, "private", "混进来的一条"), func(r *http.Request) {
			r.Header.Del(tgSecretHeader)
			if c.secret != "" {
				r.Header.Set(tgSecretHeader, c.secret)
			}
		})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s：返回 %d，想要 403（body=%s）", c.name, w.Code, w.Body.String())
		}
		if n := names(t, h.contentDir); len(n) != 0 {
			t.Errorf("%s：没通过 secret token 却写出了文件 %v", c.name, n)
		}
	}

	// 对的那个当然要放行，否则上面全绿只说明这条路由是坏的
	h := newTGHarness(t)
	h.replyOf(h.send(1, "正常一条"))
	h.only()
}

// Bearer 那条路和这条路是两套：/api/write 的 token 不能开 webhook 的门，反过来也一样。
func TestTelegramDoesNotAcceptBearerToken(t *testing.T) {
	h := newTGHarness(t)
	w := h.post(tgJSON(1, tgOwner, "private", "x"), func(r *http.Request) {
		r.Header.Del(tgSecretHeader)
		r.Header.Set("Authorization", "Bearer "+testToken)
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("返回 %d，想要 403", w.Code)
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("写出了文件 %v", n)
	}
}

// ── 第二道门：发信人 allowlist ──────────────────────────────────────────────

// 陌生人：不写文件、**不回复**（回复等于确认这个 bot 存在），只留一条日志。
// 那条日志同时是 allowlist 的引导路径——第一次配置时从里面抄自己的 user id。
func TestTelegramStrangerGetsNothing(t *testing.T) {
	h := newTGHarness(t)
	w := h.post(tgJSON(1, tgStranger, "private", "我是谁"))

	if w.Code != http.StatusOK {
		t.Errorf("返回 %d，想要 200（非 2xx 会让 Telegram 一直重投）", w.Code)
	}
	if body := w.Body.String(); strings.TrimSpace(body) != "" {
		t.Errorf("给陌生人回了话：%q —— 这等于确认这个 bot 存在", body)
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("非 allowlist 的人写出了文件 %v", n)
	}
	if logs := h.logs.String(); !strings.Contains(logs, fmt.Sprint(tgStranger)) {
		t.Errorf("日志里没有被拒的 user id，配 allowlist 时无从下手：\n%s", logs)
	}
	// 陌生人发来的正文没有理由进这台机器的日志
	if strings.Contains(h.logs.String(), "我是谁") {
		t.Errorf("陌生人的消息正文进了日志：\n%s", h.logs.String())
	}
}

// allowlist 管「谁能写」，聊天窗口决定「谁看得见那句确认」——确认里可能带着 unlisted 的
// URL，也就是那条条目的全部秘密。群里一律不动作、也不回复。
func TestTelegramIgnoresNonPrivateChats(t *testing.T) {
	for _, chatType := range []string{"group", "supergroup", "channel"} {
		h := newTGHarness(t)
		w := h.post(tgJSON(1, tgOwner, chatType, "/unlisted 群里发的"))
		if w.Code != http.StatusOK {
			t.Errorf("%s：返回 %d，想要 200", chatType, w.Code)
		}
		if body := strings.TrimSpace(w.Body.String()); body != "" {
			t.Errorf("%s：往群里回了话 %q", chatType, body)
		}
		if n := names(t, h.contentDir); len(n) != 0 {
			t.Errorf("%s：群消息写出了文件 %v", chatType, n)
		}
	}
}

// ── 落盘 ────────────────────────────────────────────────────────────────────

// 默认路径：打字、发送、完事。和 scripts/n 一致——log / private。
func TestTelegramDefaultsToPrivateLog(t *testing.T) {
	h := newTGHarness(t)
	m := h.replyOf(h.send(1, "刚想到的一件事"))

	fm, body := h.entry(h.only())
	if fm.Visibility != "private" {
		t.Errorf("落盘的 visibility = %q，想要 private —— 忘写字段时应当消失而不是泄露", fm.Visibility)
	}
	if fm.Type != "log" {
		t.Errorf("落盘的 type = %q，想要 log", fm.Type)
	}
	if fm.Title != "" {
		t.Errorf("不该凭空造一个标题：%q", fm.Title)
	}
	if body != "刚想到的一件事" {
		t.Errorf("正文 = %q", body)
	}
	if !strings.Contains(m.Text, "private") {
		t.Errorf("确认里没说可见度：%q", m.Text)
	}
	if strings.Contains(m.Text, "http") || strings.Contains(m.Text, "/e/") {
		t.Errorf("private 条目的确认里出现了 URL：%q", m.Text)
	}
	if m.ChatID != tgOwner {
		t.Errorf("回给了 chat %d，不是发信人所在的 %d", m.ChatID, tgOwner)
	}
}

// 默认路径上一个字都不解释。正文里的 # 不是标签、开头的词不是标题——
// 「打字、发送、完事」这句话如果还带着几条隐含规则，那它就不是零摩擦。
func TestTelegramPlainMessageIsNotInterpreted(t *testing.T) {
	h := newTGHarness(t)
	const text = "#include <stdio.h> 这行为什么要写在最前面\npost note public 这些词在正文里就是词"
	h.replyOf(h.send(1, text))

	fm, body := h.entry(h.only())
	if len(fm.Tags) != 0 {
		t.Errorf("正文里的 # 被当成了标签：%v", fm.Tags)
	}
	if fm.Title != "" || fm.Type != "log" || fm.Visibility != "private" {
		t.Errorf("正文里的词改了字段：title=%q type=%q vis=%q", fm.Title, fm.Type, fm.Visibility)
	}
	if body != text {
		t.Errorf("正文被改写了：\n%q\n%q", body, text)
	}
}

// 指令行的解析。每一条都对应一个「归错档」的后果，尤其是可见度那几条。
func TestTelegramDirectives(t *testing.T) {
	for _, c := range []struct {
		name  string
		text  string
		typ   string
		vis   string
		stat  string
		title string
		body  string
		tags  []string
	}{
		{
			name: "全套", text: "/post /public /growing #编译器 #tsgo 写入通道这件事\n第一段\n\n第二段",
			typ: "post", vis: "public", stat: "growing", title: "写入通道这件事",
			body: "第一段\n\n第二段", tags: []string{"编译器", "tsgo"},
		},
		{
			name: "只改可见度", text: "/public 一句公开的话",
			typ: "log", vis: "public", body: "一句公开的话",
		},
		{
			name: "只改类型", text: "/note 常青笔记\n正文",
			typ: "note", vis: "private", title: "常青笔记", body: "正文",
		},
		{
			name: "指令后面没有标题", text: "/link /public\nhttps://example.com 值得一读",
			typ: "link", vis: "public", body: "https://example.com 值得一读",
		},
		{
			name: "只有标签", text: "/private #读书\n今天读的",
			typ: "log", vis: "private", body: "今天读的", tags: []string{"读书"},
		},
		{
			name: "群里带 @bot 后缀", text: "/post@cairn_write_bot /public 标题\n正文",
			typ: "post", vis: "public", title: "标题", body: "正文",
		},
		{
			name: "大写也认", text: "/PUBLIC /Note 标题\n正文",
			typ: "note", vis: "public", title: "标题", body: "正文",
		},
		{
			name: "标题里的 # 不是标签", text: "/note 聊聊 #include 和 C#\n正文",
			typ: "note", vis: "private", title: "聊聊 #include 和 C#", body: "正文",
		},
		{
			name: "逃生舱：正文以斜杠开头", text: "//usr/local/bin 这个路径",
			typ: "log", vis: "private", body: "/usr/local/bin 这个路径",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newTGHarness(t)
			h.replyOf(h.send(1, c.text))
			fm, body := h.entry(h.only())
			if fm.Type != c.typ || fm.Visibility != c.vis || fm.Status != c.stat || fm.Title != c.title {
				t.Errorf("type=%q vis=%q status=%q title=%q\n想要 type=%q vis=%q status=%q title=%q",
					fm.Type, fm.Visibility, fm.Status, fm.Title, c.typ, c.vis, c.stat, c.title)
			}
			if body != c.body {
				t.Errorf("正文 = %q，想要 %q", body, c.body)
			}
			if strings.Join(fm.Tags, ",") != strings.Join(c.tags, ",") {
				t.Errorf("tags = %v，想要 %v", fm.Tags, c.tags)
			}
		})
	}
}

// 不认识的指令要停下来问，不能把它当标题——「/pubic 我的年薪」按标题处理的话，
// 那条本想公开的（或本想私密的）条目会落到一个谁也没打算的可见度上。
func TestTelegramRejectsUnknownDirective(t *testing.T) {
	for _, text := range []string{"/pubic 打错了", "/tweet 内容", "/id", "/公开 中文指令"} {
		h := newTGHarness(t)
		m := h.replyOf(h.send(1, text))
		if !strings.HasPrefix(m.Text, "没记下") {
			t.Errorf("%q：回了 %q，想要一句「没记下」", text, m.Text)
		}
		if !strings.Contains(m.Text, "/public") {
			t.Errorf("%q：报错里没列出可用的指令，人只能去翻文档：%q", text, m.Text)
		}
		if n := names(t, h.contentDir); len(n) != 0 {
			t.Errorf("%q：不认识的指令却写出了文件 %v", text, n)
		}
	}
}

// 同一个字段给两个值时停下来问。按「后面的赢」处理的话，/public /private 这种手滑
// 会让一条本该私密的条目安静地公开着——手机上没人会回头检查自己发出去的那行指令。
func TestTelegramRejectsConflictingDirectives(t *testing.T) {
	for _, text := range []string{"/public /private 手滑\n正文", "/post /note 标题\n正文"} {
		h := newTGHarness(t)
		m := h.replyOf(h.send(1, text))
		if !strings.HasPrefix(m.Text, "没记下") {
			t.Errorf("%q：回了 %q", text, m.Text)
		}
		if n := names(t, h.contentDir); len(n) != 0 {
			t.Errorf("%q：冲突的指令却写出了文件 %v", text, n)
		}
	}
	// 重复写同一个值不算冲突，没必要为难人
	h := newTGHarness(t)
	h.replyOf(h.send(1, "/public /public 正文"))
	if fm, _ := h.entry(h.only()); fm.Visibility != "public" {
		t.Errorf("visibility = %q", fm.Visibility)
	}
}

// unlisted：id 就是它的全部秘密。回给本人可以（是他自己要的链接），
// 但不能进日志，也不能让 Telegram 去抓那个链接做预览。
func TestTelegramUnlistedSecretGoesOnlyToTheAuthor(t *testing.T) {
	h := newTGHarness(t)
	m := h.replyOf(h.send(1, "/unlisted 只给一个人看"))

	fm, _ := h.entry(h.only())
	if fm.Visibility != "unlisted" {
		t.Fatalf("visibility = %q", fm.Visibility)
	}
	id := strings.TrimSuffix(filepath.Base(h.only()), ".md")
	if !randomID.MatchString(id) {
		t.Errorf("unlisted 的文件名 %q 不是长随机串", id)
	}
	if !strings.Contains(m.Text, "https://example.test/u/"+id+"/") {
		t.Errorf("确认里没有那条链接，本人拿不到它：%q", m.Text)
	}
	if strings.Contains(h.logs.String(), id) {
		t.Errorf("unlisted 的 id 进了日志（logID 被绕过了）：\n%s", h.logs.String())
	}
	if !m.LinkPreview.IsDisabled {
		t.Error("没关链接预览：Telegram 会用自己的服务器去抓这条 unlisted 链接并缓存")
	}
}

// public 条目要给出能点开的地址，同时说清它还没上线——写入是零摩擦的，发布还不是。
func TestTelegramPublicConfirmationCarriesURLAndCaveat(t *testing.T) {
	h := newTGHarness(t)
	m := h.replyOf(h.send(1, "/public 一条公开的"))
	id := strings.TrimSuffix(filepath.Base(h.only()), ".md")
	if !strings.Contains(m.Text, "https://example.test/e/"+id+"/") {
		t.Errorf("确认里没有 URL：%q", m.Text)
	}
	if !strings.Contains(m.Text, "publish") {
		t.Errorf("没说要重新构建才会上线，作者会点开链接看到 404：%q", m.Text)
	}
}

// ── 重投与状态码 ────────────────────────────────────────────────────────────

// Telegram 会重投同一条 update。重投一次就是磁盘上多一条一模一样的条目，
// 而且没人会发现——它看起来就像手滑发了两遍。
func TestTelegramDuplicateUpdateWritesOnce(t *testing.T) {
	h := newTGHarness(t)
	first := h.replyOf(h.send(77, "会被重投的一条"))
	if !strings.HasPrefix(first.Text, "已记下") {
		t.Fatalf("第一次投递回了 %q", first.Text)
	}

	w := h.send(77, "会被重投的一条")
	if w.Code != http.StatusOK {
		t.Errorf("重投返回 %d，想要 200", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "" {
		t.Errorf("重投又回了一句确认 %q —— 那是在撒谎，这一次什么也没写", body)
	}
	if n := names(t, h.contentDir); len(n) != 1 {
		t.Errorf("重投之后有 %d 个文件 %v，想要 1 个", len(n), n)
	}

	// 换一个 update_id 就是新的一条，去重不能把正常的新消息也吃掉
	h.replyOf(h.send(78, "另一条"))
	if n := names(t, h.contentDir); len(n) != 2 {
		t.Errorf("新的 update_id 之后有 %d 个文件 %v，想要 2 个", len(n), n)
	}
}

// Telegram 的 webhook 是并发推送的（max_connections 默认 40），
// 重投可能和第一次投递同时在跑。去重必须在动手**之前**认领。
func TestTelegramConcurrentRedeliveryWritesOnce(t *testing.T) {
	h := newTGHarness(t)
	const n = 8
	var wg sync.WaitGroup
	replies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			replies[i] = strings.TrimSpace(h.send(555, "并发重投").Body.String())
		}(i)
	}
	wg.Wait()

	confirmed := 0
	for _, r := range replies {
		if r != "" {
			confirmed++
		}
	}
	if confirmed != 1 {
		t.Errorf("%d 个并发投递里有 %d 个收到确认，想要 1 个", n, confirmed)
	}
	if got := names(t, h.contentDir); len(got) != 1 {
		t.Errorf("并发重投写出了 %d 个文件 %v", len(got), got)
	}
}

// update_id 是去重的唯一依据。它不合法就没法去重，只能拒——而拒的时候我们还没动手，
// 所以这里可以（也应该）回 4xx。
func TestTelegramRejectsUnusableUpdateID(t *testing.T) {
	h := newTGHarness(t)
	for _, body := range []string{
		`{"update_id":0,"message":{"from":{"id":4242},"chat":{"id":4242,"type":"private"},"text":"x"}}`,
		`{"update_id":-1,"message":{"from":{"id":4242},"chat":{"id":4242,"type":"private"},"text":"x"}}`,
		`{"message":{"from":{"id":4242},"chat":{"id":4242,"type":"private"},"text":"x"}}`,
	} {
		if w := h.post(body); w.Code != http.StatusBadRequest {
			t.Errorf("update_id 不合法却返回 %d，想要 400（body=%s）", w.Code, body)
		}
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("写出了文件 %v", n)
	}
}

func TestTelegramMalformedAndOversizedBodies(t *testing.T) {
	h := newTGHarness(t)
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"截断的 JSON", `{"update_id":1,"message":`, http.StatusBadRequest},
		{"根本不是 JSON", "not json at all", http.StatusBadRequest},
		{"空 body", "", http.StatusBadRequest},
		{"类型不对的 update_id", `{"update_id":"1"}`, http.StatusBadRequest},
	} {
		if got := h.post(c.body).Code; got != c.want {
			t.Errorf("%s：返回 %d，想要 %d", c.name, got, c.want)
		}
	}

	// 真的 Telegram 消息最多 4096 字符，超限只可能来自别处
	big := tgJSON(1, tgOwner, "private", strings.Repeat("字", 200<<10))
	if got := h.post(big).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("超大 update 返回 %d，想要 413", got)
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("畸形／超大的请求写出了文件 %v", n)
	}
}

// 处理失败也要回 200。非 2xx 会让 Telegram 一直重投，而这条通道最常见的永久性失败
// （权限、非法字段）重投一万次也还是失败。失败要在聊天里说，不是在状态码里说。
func TestTelegramAnswers200EvenWhenTheWriteFails(t *testing.T) {
	h := newTGHarness(t)
	for i, c := range []struct{ name, text string }{
		{"标签做不出 slug", "/public #!!!\n正文"},
		{"空正文", "/post"},
		{"指令行之后什么也没有", "/post /public\n"},
	} {
		w := h.send(int64(i+1), c.text)
		if w.Code != http.StatusOK {
			t.Errorf("%s：返回 %d，想要 200 —— 非 2xx 会引来无休止的重投", c.name, w.Code)
		}
		var m tgSendMessage
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s：响应不是 sendMessage：%q", c.name, w.Body.String())
		}
		if !strings.HasPrefix(m.Text, "没记下") {
			t.Errorf("%s：回了 %q，本人不知道这条没记上", c.name, m.Text)
		}
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("失败的请求写出了文件 %v", n)
	}
}

// ── 其它更新类型 ────────────────────────────────────────────────────────────

// /help 和 /start 只回一句话，不落条目。
func TestTelegramHelpWritesNothing(t *testing.T) {
	h := newTGHarness(t)
	for i, text := range []string{"/help", "/start", "/help@cairn_write_bot", "/START"} {
		m := h.replyOf(h.send(int64(i+1), text))
		if m.Text != tgHelp {
			t.Errorf("%q 回的不是帮助：%q", text, m.Text)
		}
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("/help 写出了条目 %v", n)
	}
}

// 图片、语音、贴纸都没有 text。要说一句，不然本人以为记上了。
func TestTelegramNonTextMessage(t *testing.T) {
	h := newTGHarness(t)
	m := h.replyOf(h.post(tgJSON(1, tgOwner, "private", "")))
	if !strings.Contains(m.Text, "纯文本") {
		t.Errorf("回了 %q", m.Text)
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("没有正文的消息写出了文件 %v", n)
	}
}

// 我们只订阅 message。别的更新类型（edited_message、channel_post……）要静默放过，
// 而且**不能**因为结构里有不认识的字段就 400——Bot API 每个版本都在加字段，
// 严格解析等于「Telegram 一升级，手机写入就全挂」，还是静默地挂。
func TestTelegramIgnoresOtherUpdateTypes(t *testing.T) {
	h := newTGHarness(t)
	for i, body := range []string{
		`{"update_id":1,"edited_message":{"from":{"id":4242},"chat":{"id":4242,"type":"private"},"text":"改过的"}}`,
		`{"update_id":2,"channel_post":{"chat":{"id":-100,"type":"channel"},"text":"频道"}}`,
		`{"update_id":3,"my_chat_member":{"from":{"id":4242}}}`,
		`{"update_id":4,"message":{"chat":{"id":4242,"type":"private"},"text":"没有 from"}}`,
		`{"update_id":5,"future_field_from_bot_api_99":{"whatever":true}}`,
	} {
		w := h.post(body)
		if w.Code != http.StatusOK {
			t.Errorf("第 %d 条：返回 %d，想要 200（body=%s）", i, w.Code, w.Body.String())
		}
		if got := strings.TrimSpace(w.Body.String()); got != "" {
			t.Errorf("第 %d 条：回了话 %q", i, got)
		}
	}
	if n := names(t, h.contentDir); len(n) != 0 {
		t.Errorf("写出了文件 %v", n)
	}

	// 正常消息里混进不认识的新字段，必须照常落盘
	w := h.post(`{"update_id":9,"message":{"message_id":1,"from":{"id":4242,"is_bot":false,` +
		`"brand_new_field":1},"chat":{"id":4242,"type":"private"},"text":"未来的 Bot API",` +
		`"quote":{"text":"x"}},"unknown_top_level":{"a":1}}`)
	h.replyOf(w)
	if _, body := h.entry(h.only()); body != "未来的 Bot API" {
		t.Errorf("正文 = %q", body)
	}
}

// ── 配置 ────────────────────────────────────────────────────────────────────

// 半配是唯一危险的状态：填了名单忘了 secret，等于把一个能写磁盘的端点挂在公网上。
func TestTelegramConfigRefusesToRunUnsafely(t *testing.T) {
	for _, c := range []struct{ name, secret, allow string }{
		{"有名单没 secret", "", "4242"},
		{"有 secret 没名单", testTGSecret, ""},
		{"secret 太短", "short", "4242"},
		{"secret 里有 Telegram 不接受的字符", strings.Repeat("a", 31) + "!", "4242"},
		{"名单填了 @用户名", testTGSecret, "@someone"},
		{"名单填了 0", testTGSecret, "0"},
		{"名单填了负数", testTGSecret, "-4242"},
		{"名单只有分隔符", testTGSecret, ", ,"},
	} {
		t.Setenv("CAIRN_TELEGRAM_SECRET", c.secret)
		t.Setenv("CAIRN_TELEGRAM_ALLOW", c.allow)
		if _, err := loadTelegramConfig(); err == nil {
			t.Errorf("%s：居然起来了", c.name)
		}
	}

	// 两个都不配 = 不开这条通道，不是错误
	t.Setenv("CAIRN_TELEGRAM_SECRET", "")
	t.Setenv("CAIRN_TELEGRAM_ALLOW", "")
	tg, err := loadTelegramConfig()
	if err != nil || tg.enabled {
		t.Errorf("都不配时 enabled=%v err=%v，想要 false / nil", tg.enabled, err)
	}

	// 配齐了就认，逗号 / 空格 / 换行都当分隔符
	t.Setenv("CAIRN_TELEGRAM_SECRET", testTGSecret)
	t.Setenv("CAIRN_TELEGRAM_ALLOW", "4242, 88\n99")
	tg, err = loadTelegramConfig()
	if err != nil {
		t.Fatalf("配齐了却报错：%v", err)
	}
	if !tg.enabled || len(tg.allow) != 3 || !tg.allow[4242] || !tg.allow[88] || !tg.allow[99] {
		t.Errorf("名单解析成了 %v", tg.allow)
	}
}

// 半配必须在 loadConfig 这一层就把服务拦下来，而不是只有直接调 loadTelegramConfig 才拦。
func TestLoadConfigPropagatesTelegramFailure(t *testing.T) {
	t.Setenv("CAIRN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("CAIRN_TELEGRAM_ALLOW", "4242")
	t.Setenv("CAIRN_TELEGRAM_SECRET", "")
	if _, err := loadConfig(); err == nil {
		t.Error("配了名单没配 secret，服务居然起来了")
	}
}

// 没配 Telegram 时这条路由**根本不该存在**。注册一个「暂不可用」的端点等于告诉扫描器
// 这里有东西；而且将来一次重构漏掉 enabled 判断，就成了一个无鉴权的写入口。
func TestTelegramRouteAbsentWhenNotConfigured(t *testing.T) {
	cfg := config{token: testToken, contentDir: filepath.Join(t.TempDir(), "entries")}
	srv := withLogging(routes(cfg))
	r := httptest.NewRequest(http.MethodPost, "/api/telegram", strings.NewReader(`{"update_id":1}`))
	r.Header.Set(tgSecretHeader, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("没配 Telegram 时 /api/telegram 返回 %d，想要 404", w.Code)
	}
	if n := names(t, cfg.contentDir); len(n) != 0 {
		t.Errorf("写出了文件 %v", n)
	}
}

// 指令词表是从 write.go 的三张校验表生成的。三张表里出现同名值就意味着
// 「/xxx 设哪个字段」变成掷骰子——init 里会 panic，这里把那条不变量写死。
func TestTelegramDirectiveWordsAreDistinctAndComplete(t *testing.T) {
	want := map[string]string{}
	for v := range validTypes {
		want[v] = "type"
	}
	for v := range validVisibility {
		if _, dup := want[v]; dup {
			t.Fatalf("%q 同时是 type 和 visibility 的值，指令词表会有歧义", v)
		}
		want[v] = "visibility"
	}
	for v := range validStatus {
		if v == "" {
			continue
		}
		if _, dup := want[v]; dup {
			t.Fatalf("%q 同时是别的字段和 status 的值，指令词表会有歧义", v)
		}
		want[v] = "status"
	}
	if len(tgDirectives) != len(want) {
		t.Errorf("词表有 %d 个词，字段值有 %d 个", len(tgDirectives), len(want))
	}
	for v, field := range want {
		d, ok := tgDirectives[v]
		if !ok {
			t.Errorf("/%s 不在指令词表里，手机上没法设这个值", v)
			continue
		}
		if d.field != field || d.value != v {
			t.Errorf("/%s 设的是 %s=%s，想要 %s=%s", v, d.field, d.value, field, v)
		}
	}
	// 帮助里必须把这些词列全，否则「要记的语法」就只能靠翻源码
	for v := range want {
		if !strings.Contains(tgHelp, "/"+v) {
			t.Errorf("/%s 没写进 /help", v)
		}
	}
}

// 去重缓存是有界的，最老的会被挤出去。挤出去之后**不能**把还在里面的那些一起带走。
func TestUpdateCacheEviction(t *testing.T) {
	u := newUpdateCache(4)
	for i := int64(1); i <= 4; i++ {
		if !u.claim(i) {
			t.Fatalf("update %d 第一次就被当成重复", i)
		}
	}
	for i := int64(1); i <= 4; i++ {
		if u.claim(i) {
			t.Errorf("update %d 没被记住", i)
		}
	}
	if !u.claim(5) { // 挤掉 1
		t.Fatal("update 5 第一次就被当成重复")
	}
	if !u.claim(1) {
		t.Error("1 被挤出去之后应当重新可用（有界缓存的代价，写在注释里）")
	}
	for _, i := range []int64{3, 4, 5} {
		if u.claim(i) {
			t.Errorf("update %d 被误挤掉了", i)
		}
	}
}
