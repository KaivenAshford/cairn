package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxBodyBytes = 256 << 10 // 一条记录不该有 256 KiB
	dateLayout   = "2006-01-02"
)

var (
	validTypes      = map[string]bool{"post": true, "note": true, "log": true, "link": true, "list": true, "photo": true}
	validVisibility = map[string]bool{"public": true, "unlisted": true, "circle": true, "private": true}
	validStatus     = map[string]bool{"": true, "seed": true, "growing": true, "done": true}

	// 条目 id 直接就是文件名和 URL 的一段。用白名单而不是「过滤掉危险字符」：
	// 白名单不会因为想漏了某种编码或分隔符而失守。
	validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

	// 与 web/src/lib/entries.ts 的 RANDOM_ID 保持一致。两处都要有：
	// 服务端拦住写入那一刻，构建侧拦住手写和改名。
	randomID = regexp.MustCompile(`^[0-9a-f]{32,}$`)
)

// writeMu 串行化整个读-改-写。见 handleWrite 里的注释。
var writeMu sync.Mutex

// requireToken 挡在写入通道前面。用固定时间比较，避免逐字节试探。
// （长度不等时 subtle 会提前返回，所以 token 的长度仍可被计时区分——长度信息本身无用，
// 内容才是秘密。）
func (c config) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, prefix)
		if !strings.HasPrefix(header, prefix) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(c.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cairn"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type writeRequest struct {
	// ID 为空 = 新建一条（id 由标题或随机数生成，撞名时拒绝）。
	// 给了 ID = 更新那一条（不存在就按这个 id 新建）。见 handleWrite 的注释。
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	Visibility string   `json:"visibility"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
}

type writeResponse struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Visibility string `json:"visibility"`
	Action     string `json:"action"` // created | updated
	URL        string `json:"url,omitempty"`
}

// handleWrite 是这个站的心脏：手机上一条消息 / 终端一个命令 → 磁盘上一条 markdown。
// 记录类内容的唯一死因是输入摩擦，所以这条链路要短到没有借口不用。
//
// 两种意图，由请求里有没有 id 区分：
//
//	没有 id  新建。id 自动生成，撞上已有条目一律拒绝（409），绝不覆盖。
//	有  id  更新那一条。没提供的字段沿用旧值，created 保留，updated 记为今天。
//	         那条不存在时就按这个 id 新建。
//
// 分开的理由：/now 这类「会被覆盖、不留历史」的单页必须能改，而自动生成 id 的
// 那条路径上，覆盖只可能是撞名事故——两种意图用同一个语义，总有一种是错的。
func (c config) handleWrite(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var req writeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// 「你的 JSON 写错了」和「你的内容太长了」是两回事，客户端得能分开处理。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("请求体超过上限 %d KiB", maxBodyBytes>>10), http.StatusRequestEntityTooLarge)
			return
		}
		badRequest(w, fmt.Sprintf("请求体解析失败：%v", err))
		return
	}

	if strings.TrimSpace(req.Text) == "" {
		badRequest(w, "text 不能为空")
		return
	}
	if req.ID != "" && !validID.MatchString(req.ID) {
		badRequest(w, fmt.Sprintf("id 只能用小写字母、数字和连字符，且不超过 63 字符：%q", req.ID))
		return
	}

	// 更新是一次读-改-写：先读旧条目拿到它的 visibility/type/title，再整个覆盖回去。
	// 两个请求交错的话，「把这条收回成 private」会被另一条沿用旧值的编辑悄悄写回 public。
	// 单用户、每秒不到一次的接口，一把全局锁就够，不值得为它引入 per-id 锁。
	writeMu.Lock()
	defer writeMu.Unlock()

	var old *existingEntry
	if req.ID != "" {
		found, err := c.findEntry(req.ID)
		switch {
		case errors.Is(err, errUnparsable):
			// 不能当成 500：服务器没坏，是这条已有条目的 frontmatter 需要人去看一眼。
			log.Printf("拒绝更新 %s：%v", req.ID, err)
			http.Error(w, "这条已有条目的 frontmatter 解析不了，拒绝更新以免抹掉它的旧字段", http.StatusConflict)
			return
		case err != nil:
			log.Printf("查找条目 %s 失败：%v", req.ID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		old = found
	}

	// 更新一条已有条目时，没提供的字段沿用旧值。
	//
	// visibility 这一条值得单独说：更新时忘写它应当**保持原样**，而不是掉回 private。
	// 「默认 private」防的是新建时忘写字段导致泄露；而把一条已经公开的条目改成私有，
	// 是让它从站上悄悄消失——那不是安全，那是数据丢失。两种默认值服务于同一个原则：
	// 不因为漏写字段而改变可见度。
	if old != nil {
		if req.Type == "" {
			req.Type = old.fm.Type
		}
		if req.Title == "" {
			req.Title = old.fm.Title
		}
		if req.Visibility == "" {
			req.Visibility = old.fm.Visibility
		}
		if req.Status == "" {
			req.Status = old.fm.Status
		}
		if req.Tags == nil {
			req.Tags = old.fm.Tags
		}
	}

	if req.Type == "" {
		req.Type = "log"
	}
	// 默认 private，与 web/src/content.config.ts 里的默认值一致：
	// 忘了写这个字段时应当「消失」，而不是「泄露」。要公开必须显式声明。
	if req.Visibility == "" {
		req.Visibility = "private"
	}

	if !validTypes[req.Type] {
		badRequest(w, fmt.Sprintf("未知的 type：%q", req.Type))
		return
	}
	if !validVisibility[req.Visibility] {
		badRequest(w, fmt.Sprintf("未知的 visibility：%q", req.Visibility))
		return
	}
	if !validStatus[req.Status] {
		badRequest(w, fmt.Sprintf("未知的 status：%q", req.Status))
		return
	}

	for _, t := range req.Tags {
		if tagSlug(t) == "" {
			badRequest(w, fmt.Sprintf(
				"标签 %q 里没有任何字母、数字或中文，做不出 URL 里的一段。\n"+
					"标签会变成 /t/<标签>/ 这个页面的地址。", t))
			return
		}
	}

	// 自动生成 id 那条路径上，unlisted 一定拿到 randomHex(16)。但显式给了 id 就绕过了
	// entryID，于是 `n -i salary-numbers -v unlisted` 会落一个猜得到的文件名。
	// 构建那一关（web/src/lib/entries.ts 的 RANDOM_ID）确实会拦住，但那时已经晚了：
	// 写入回的是 201 和一个 /u/salary-numbers/ 的 URL，而下一次构建整站失败，
	// 那条本想收起来的内容仍然挂在原来的公开地址上。所以这里就要拒绝。
	if req.Visibility == "unlisted" && req.ID != "" && !randomID.MatchString(req.ID) {
		badRequest(w, fmt.Sprintf(
			"unlisted 的文件名就是 URL，必须是 32 位以上十六进制随机串（openssl rand -hex 16）：%q\n"+
				"新建 unlisted 不要给 id，服务端会生成；想把已公开的条目收起来请用 visibility: private。",
			req.ID))
		return
	}

	now := time.Now()
	created, updated := now.Format(dateLayout), ""
	id := req.ID

	switch {
	case old != nil:
		// 更新：保留原始创建日期，记下改动日期。
		if old.fm.Created != "" {
			created = old.fm.Created
		}
		updated = now.Format(dateLayout)
	case id == "":
		var err error
		if id, err = entryID(req, now); err != nil {
			log.Printf("生成 id 失败：%v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := os.MkdirAll(c.contentDir, contentDirMode); err != nil {
		log.Printf("创建目录 %s 失败：%v", c.contentDir, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	path := filepath.Join(c.contentDir, id+".md")

	// 新建时用 exclusive：目标已存在就失败，由内核保证，不存在检查与写入之间的窗口。
	// 更新时不用：调用方给了明确的 id，覆盖正是它要的。
	if err := atomicWrite(path, buildEntry(req, created, updated), contentFileMode, old == nil); err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "该 id 已存在", http.StatusConflict)
			return
		}
		log.Printf("写入 %s 失败：%v", path, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	action, verb := "created", "新建"
	status := http.StatusCreated
	if old != nil {
		action, verb, status = "updated", "更新", http.StatusOK
	}

	resp := writeResponse{ID: id, Path: path, Visibility: req.Visibility, Action: action}
	switch req.Visibility {
	case "public":
		resp.URL = "/e/" + id + "/"
	case "unlisted":
		resp.URL = "/u/" + id + "/"
	}

	log.Printf("%s条目 %s（%s / %s）", verb, logID(id, req.Visibility), req.Type, req.Visibility)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// tagUnsafe 与 web/src/lib/entries.ts 的 tagSlug 保持同一套规则：
// 标签会变成 /t/<slug>/ 这个路由的参数，也就是 dist 下的目录名，
// 所以除字母、数字、中文之外的字符一律折成 `-`。
var tagUnsafe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// tagSlug 是 web/src/lib/entries.ts 里那个函数的 Go 版本。**两边必须一致**——
// 和「两处默认值都是 private」同一个道理：构建那一端已经会为坏标签大声失败，
// 但那时候东西已经落盘了。写入这一端不拦的话，手机上发一条标签手滑的记录会返回 201，
// 而下一次 scripts/publish 构建失败、拒绝换产物，整站冻结在上一版，
// 直到有人 ssh 上去手改那个 md。非法 type 是当场 400 的，标签没有理由例外。
func tagSlug(tag string) string {
	s := tagUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(tag)), "-")
	return strings.Trim(s, "-")
}

// logID 决定一个 id 能不能进日志。
// unlisted 条目的 id 就是 /u/<id>/ 这条 URL 的全部秘密（见 ARCHITECTURE.md 第 2 节）。
// 把它打进 docker logs / journald / 任何日志聚合，等于让这个秘密离开内容仓库的信任边界。
// 排查问题需要的是「有没有写成功、是什么类型」，不需要知道是哪一条。
func logID(id, visibility string) string {
	if visibility == "unlisted" {
		return "<unlisted:" + strconv.Itoa(len(id)) + ">"
	}
	return id
}

type existingEntry struct {
	path string
	fm   frontmatter
}

// errUnparsable：已有条目的 frontmatter 读不懂。
//
// 这个错误必须存在，不能让「读不懂」伪装成「旧值都是空的」：那样一次只改正文的更新
// 会把 title/type/status/created 全部抹掉、visibility 掉回 private，而 HTTP 还是 200。
// 触发它不需要什么畸形文件——CRLF 行尾、UTF-8 BOM、开分隔符行尾多一个空格都够了，
// 而这三种文件在 Astro 眼里都是完全合法的条目，构建照常通过。
var errUnparsable = errors.New("已有条目的 frontmatter 解析不了")

func (c config) findEntry(id string) (*existingEntry, error) {
	p := filepath.Join(c.contentDir, id+".md")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fm, ok := parseFrontmatter(string(b))
	if !ok {
		return nil, fmt.Errorf("%w：%s", errUnparsable, p)
	}
	return &existingEntry{path: p, fm: fm}, nil
}

func badRequest(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusBadRequest)
}

func buildEntry(req writeRequest, created, updated string) string {
	var b strings.Builder
	b.WriteString("---\n")
	if req.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", yamlString(req.Title))
	}
	fmt.Fprintf(&b, "type: %s\n", req.Type)
	fmt.Fprintf(&b, "visibility: %s\n", req.Visibility)
	if req.Status != "" {
		fmt.Fprintf(&b, "status: %s\n", req.Status)
	}
	fmt.Fprintf(&b, "created: %s\n", created)
	if updated != "" {
		fmt.Fprintf(&b, "updated: %s\n", updated)
	}
	if len(req.Tags) > 0 {
		quoted := make([]string, len(req.Tags))
		for i, t := range req.Tags {
			quoted[i] = yamlString(t)
		}
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(quoted, ", "))
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(req.Text, "\n"))
	b.WriteString("\n")
	return b.String()
}

// yamlString 用双引号包裹并转义。写入的是用户内容，不能指望它「看起来像标量」。
//
// 控制字符必须转义，不能原样写出去：YAML 解析器直接拒绝双引号标量里的 C0 字符
// （「unacceptable character #x001b」）。而它们进得来——从终端粘一段带 ANSI 颜色码的
// 输出当标题就够了。真让它落盘，坏的不是这一条：下一次 astro build 会在 YAML 解析这步
// 整个失败，站上所有内容一起消失，而错误信息指向的是解析器而不是这条记录。
func yamlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n' || r == '\r':
			b.WriteByte(' ') // 标量必须留在一行内；标题里的换行没有意义
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == 0x85 || r == 0x2028 || r == 0x2029:
			// YAML 也把这三个当换行符，留着会把标量截断
			fmt.Fprintf(&b, `\u%04x`, r)
		case r == utf8.RuneError:
			b.WriteString(`�`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify 把标题压成 URL 片段，只保留 ASCII 字母数字。
func slugify(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s
}

// slugFor 决定标题该不该用来当 id。
//
// 只看 slugify 的结果是否为空是不够的：「中文标题不进 URL」会剩下一个 `url`,
// 于是 /e/2026-09-04-url/ ——既说不出这条是什么，又极容易和同一天的其它条目撞名
// （中文标题里夹带项目名、英文缩写太常见了）。所以还要求 slug 真的还原了标题的大部分，
// 否则退化到随机后缀：一个无意义的随机串至少不假装自己有意义。
func slugFor(title string) string {
	slug := slugify(title)
	if len(slug) < 3 {
		return ""
	}
	kept, total := 0, 0
	for _, r := range strings.TrimSpace(title) {
		total++
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			kept++
		}
	}
	if total > 0 && kept*2 < total {
		return ""
	}
	return slug
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func entryID(req writeRequest, now time.Time) (string, error) {
	// unlisted 的文件名就是 URL，它的全部防护就是「猜不到」，所以必须足够随机。
	// 真正泄露了有后果的东西该用 circle/private，走服务端认证。
	if req.Visibility == "unlisted" {
		return randomHex(16)
	}
	date := now.Format(dateLayout)
	if slug := slugFor(req.Title); slug != "" {
		return date + "-" + slug, nil
	}
	suffix, err := randomHex(4)
	if err != nil {
		return "", err
	}
	return date + "-" + suffix, nil
}

// 整个内容目录是一个 private 仓库，里面混着四层可见度的条目。
// 权限按最敏感的那层给：同机其它用户不该读到任何一条。
const (
	contentDirMode  os.FileMode = 0o700
	contentFileMode os.FileMode = 0o600
)

// atomicWrite 先写同目录的临时文件再落到最终路径：构建器可能正在读这个目录，
// 不能让它看到写了一半的 markdown。
//
// exclusive 为真时用 os.Link 而不是 os.Rename。rename 会静默覆盖已存在的目标，
// 于是「先 Stat 确认不存在、再 Rename」之间有一个窗口：并发写同一个 id 时两个请求
// 都能通过存在性检查，后到的那个覆盖掉先到的内容，却仍然回 201。实测 12 路并发写同一
// 标题会得到 9 个「已创建」而磁盘上只剩 1 个文件——调用方不会重试，内容就这么没了。
// os.Link 把这个判断交给内核，目标已存在直接 EEXIST，没有窗口。
func atomicWrite(path, content string, mode os.FileMode, exclusive bool) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".cairn-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // rename 成功后是 no-op;link 成功后删掉多余的那个名字

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}

	if exclusive {
		if err := os.Link(tmp, path); err != nil {
			return err
		}
	} else if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir 把目录项本身刷到盘上。没有这一步，断电后可能文件内容在、目录里却没有它。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// frontmatter 是从已有条目里捞回来的旧值，只在更新时用。
type frontmatter struct {
	Title      string
	Type       string
	Visibility string
	Status     string
	Created    string
	Tags       []string
}

// parseFrontmatter 认得 buildEntry 写出的格式，以及手写 markdown 里常见的裸标量
// （`title: 现在`）。它只认这几个键，拿不准的一律忽略——唯一职责是更新条目时把旧值捞回来，
// 不是一个通用 YAML 实现。
//
// 但「拿不准就忽略」只适用于**单个字段**：整块读不懂时必须返回 false，让调用方拒绝更新。
// 不能指望「构建时 Astro 会大声失败」兜底——BOM / CRLF / 分隔符尾随空格这三种情况下
// Astro 构建完全正常，站上那条内容活得好好的，只有这里读不懂。
func parseFrontmatter(content string) (frontmatter, bool) {
	var fm frontmatter

	// 先归一化三种「在 Astro 眼里完全合法、在这里却会失配」的写法：UTF-8 BOM、
	// CRLF 行尾、分隔符行尾的空格。它们都不是畸形文件——Windows 编辑器、一次复制粘贴
	// 就能产生，而且构建照常通过。不归一化的话，这种条目会被判成「解析不了」，
	// 于是一次只改正文的更新被拒（409），人却不知道自己哪里做错了。
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")

	first, rest, ok := strings.Cut(content, "\n")
	if !ok || strings.TrimRight(first, " \t") != "---" {
		return fm, false
	}

	lines := strings.Split(rest, "\n")
	closed := false
	for _, line := range lines {
		if strings.TrimRight(line, " \t") == "---" {
			closed = true
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "title":
			fm.Title = unquoteYAML(val)
		case "type":
			fm.Type = unquoteYAML(val)
		case "visibility":
			fm.Visibility = unquoteYAML(val)
		case "status":
			fm.Status = unquoteYAML(val)
		case "created":
			fm.Created = unquoteYAML(val)
		case "tags":
			fm.Tags = parseYAMLList(val)
		}
	}
	// 没有闭合分隔符就不是 frontmatter，别把正文当成字段读进来。
	return fm, closed
}

// unquoteYAML 反转 yamlString。裸标量原样返回。
func unquoteYAML(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' || i+1 >= len(inner) {
			b.WriteByte(inner[i])
			continue
		}
		i++
		switch inner[i] {
		case 't':
			b.WriteByte('\t')
		case 'x':
			if i+2 < len(inner) {
				if v, err := strconv.ParseUint(inner[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteByte(inner[i])
		case 'u':
			if i+4 < len(inner) {
				if v, err := strconv.ParseUint(inner[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(v))
					i += 4
					continue
				}
			}
			b.WriteByte(inner[i])
		default:
			b.WriteByte(inner[i]) // \\ 和 \" 走这里
		}
	}
	return b.String()
}

// parseYAMLList 解析 `[a, "b, c"]` 这种单行流式序列。引号内的逗号不算分隔符。
func parseYAMLList(s string) []string {
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := s[1 : len(s)-1]
	var out []string
	var cur strings.Builder
	inQuote, escaped := false, false
	flush := func() {
		if item := unquoteYAML(strings.TrimSpace(cur.String())); item != "" {
			out = append(out, item)
		}
		cur.Reset()
	}
	for i := 0; i < len(inner); i++ {
		ch := inner[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inQuote:
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ',' && !inQuote:
			flush()
			continue
		}
		cur.WriteByte(ch)
	}
	flush()
	return out
}
