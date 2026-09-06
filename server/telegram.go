// cairn 的 Telegram 写入通道——README「核心判断」里的手机那一半。
//
// 终端那一半是 scripts/n，早就跑通了；手机这一半一直空着，而它才是这条判断真正指向的
// 场景：记录类内容的唯一死因是「记一条要开电脑、进后台、填表单」。所以这条路径上的
// 默认动作必须是**打字、发送、完事**——不带任何前缀的一条消息就该落成一条条目。
//
// 形态是 webhook（Telegram 主动 POST 过来），不是长轮询。长轮询要一个常驻的出站循环、
// 要自己管 offset 和退避，而这个服务的全部价值在于「它在那儿、不用管」。
// webhook 的代价是这个地址公网可达，所以下面两道门缺一不可，而且缺了就拒绝启动。
//
// # 两道门
//
//  1. secret token。setWebhook 时登记一个 secret_token，此后 Telegram 每次推送都会带
//     X-Telegram-Bot-Api-Secret-Token 头。它证明的是「这个请求来自 Telegram」。
//  2. 发信人 allowlist。它证明的是「这条消息来自我」。缺了第二道，任何知道 bot 用户名的
//     人都能往站里写——secret token 只挡住了「不是 Telegram 发来的」，挡不住
//     「是 Telegram 发来的，但发信的是陌生人」。
//
// 这和 ARCHITECTURE.md 第 3 节是同一个思路：allowlist，不做用户系统。那里回答的是
// 「我知道我想给谁看」，这里回答的是「我知道谁能往我的站里写」。
//
// # 回复走 webhook 响应体，不回调 Bot API
//
// Telegram 允许把一次方法调用直接写进 webhook 的响应体（body 里带 method 字段）。
// 这里用这条路，于是：服务端**不需要保存 bot token**（那是唯一一个能冒充这个 bot 的
// 凭证，少存一个就少一个泄露面），不需要出站 HTTP（零依赖仍然是零，容器也不需要出网），
// 没有超时、重试、goroutine 泄漏可写错。
// 代价是回复失败时我们不会知道——可以接受：条目已经落盘了，那句确认只是确认。
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 见 updateCache 的注释。
const updateCacheSize = 256

// secret token 的下限。Telegram 只要求 1–256 个字符，那等于没要求：
// 这个头就是「你是不是 Telegram」的全部证据，短到能猜的话，两道门就只剩一道。
// 和 CAIRN_TOKEN 用同一个下限，同一个理由。
const minTelegramSecret = 32

// tgSecretHeader 是 Telegram 带 secret token 的头。
const tgSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// telegramConfig 为空（enabled 为 false）时，/api/telegram 这条路由**根本不注册**。
// 这和 /circle/* 返回 501 是两回事：501 说的是「这东西计划要做、还没做，先拒绝」，
// 而没配 Telegram 说的是「这条通道我不用」——不用的通道不该有一个端点在那儿应答。
type telegramConfig struct {
	enabled bool
	secret  string
	allow   map[int64]bool
}

// loadTelegramConfig 读两个环境变量，并且**只接受全配或全不配**。
//
// 半配是唯一危险的状态：填了 allowlist 忘了 secret，就等于把一个能往磁盘写文件的
// 端点挂在公网上，靠「没人知道这个路径」活着。所以只要沾了一个就必须两个都对，
// 否则拒绝启动——和 CAIRN_TOKEN 缺失时拒绝启动是同一条规矩。
func loadTelegramConfig() (telegramConfig, error) {
	var tg telegramConfig
	secret := os.Getenv("CAIRN_TELEGRAM_SECRET")
	list := os.Getenv("CAIRN_TELEGRAM_ALLOW")

	if secret == "" && list == "" {
		return tg, nil // 不开这条通道
	}
	if secret == "" {
		return tg, errors.New("配了 CAIRN_TELEGRAM_ALLOW 却没有 CAIRN_TELEGRAM_SECRET；" +
			"没有它，任何人都能往这个公网端点投递假的 update。用 `openssl rand -hex 32` 生成一个")
	}
	if len(secret) < minTelegramSecret {
		return tg, fmt.Errorf("CAIRN_TELEGRAM_SECRET 太短，至少 %d 个字符", minTelegramSecret)
	}
	// Telegram 只接受 A-Z a-z 0-9 _ - 这几类字符。不在这里挡的话，服务照常起来，
	// 而错误要到部署那天调 setWebhook 时才冒出来，报的还是 Telegram 那边的话。
	for _, r := range secret {
		ok := r == '_' || r == '-' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return tg, fmt.Errorf("CAIRN_TELEGRAM_SECRET 里有 Telegram 不接受的字符 %q，"+
				"只能用 A-Z a-z 0-9 _ -（`openssl rand -hex 32` 的产物一定合法）", r)
		}
	}

	allow, err := parseAllowlist(list)
	if err != nil {
		return tg, err
	}
	if len(allow) == 0 {
		return tg, errors.New("CAIRN_TELEGRAM_ALLOW 是空的；" +
			"空名单等于谁都能写，不如不开这条通道。第一次给 bot 发消息后，" +
			"服务端日志里会有那条被拒消息的用户 id，抄进来即可")
	}

	tg.enabled, tg.secret, tg.allow = true, secret, allow
	return tg, nil
}

// parseAllowlist 认逗号、空格、换行分隔的 Telegram 数字用户 id。
//
// 只认数字 id，不认 @username：username 能被本人改掉，放弃之后还能被别人注册走。
// 拿它当身份，等于把「谁能往我的站里写」挂在一个可转让的名字上。数字 id 不会变。
func parseAllowlist(s string) (map[int64]bool, error) {
	allow := map[int64]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("CAIRN_TELEGRAM_ALLOW 里的 %q 不是一个 Telegram 数字用户 id；"+
				"这里只能填数字（不是 @用户名）", f)
		}
		allow[id] = true
	}
	return allow, nil
}

// ── Telegram 的数据结构。只声明用得上的字段 ──────────────────────────────────

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	From *tgUser `json:"from"`
	Chat *tgChat `json:"chat"`
	Text string  `json:"text"`
}

type tgUser struct {
	ID int64 `json:"id"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// tgSendMessage 是写在 webhook 响应体里的那次方法调用。
type tgSendMessage struct {
	Method string `json:"method"` // 固定 sendMessage
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
	// 必须关掉链接预览。unlisted 条目的 URL 就是它的全部秘密，而 Telegram 为了做预览
	// 会用自己的服务器去抓那个地址——等于把一个「靠猜不到」的链接主动交给第三方抓取并缓存。
	// 这条和 logID 防的是同一件事：那个 id 只该出现在作者眼前，不该出现在任何别的地方。
	LinkPreview tgLinkPreviewOptions `json:"link_preview_options"`
}

type tgLinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// ── 消息语法 ────────────────────────────────────────────────────────────────

// tgDirectives 是指令词表：**每个词就是 frontmatter 里那个字段的值本身**。
//
// 这是这套语法唯一的规则。没有 key=value，没有短横线开关，也没有别名——
// 因为这条路径的全部意义是低摩擦，而一套要背的语法本身就是摩擦。十三个字段值互不重名，
// 所以「/post 设的是哪个字段」无歧义；要记的东西于是不是一套新语法，
// 而是你写 frontmatter 时本来就在用的那几个词。
//
// 词表从 write.go 的三张校验表生成，不手抄：以后多一个 type 值（ARCHITECTURE 第 4 节
// 说过「加一种新内容类型 = 多一个 type 值，架构不动」），手机这一半自动跟上。
var tgDirectives = map[string]tgDirective{}

type tgDirective struct{ field, value string }

func init() {
	add := func(field string, values map[string]bool) {
		for v := range values {
			if v == "" {
				continue // validStatus 里的空串是「没写 status」，不是一个词
			}
			if old, dup := tgDirectives[v]; dup {
				// 词表撞名 = 「/xxx 设哪个字段」变成掷骰子。宁可起不来：
				// 这个 panic 在任何一次 go test 里都会立刻炸，而静默覆盖要到有人
				// 发了一条被归错档的消息才会被发现——如果还发现得了的话。
				panic(fmt.Sprintf("Telegram 指令词 %q 同时属于 %s 和 %s", v, old.field, field))
			}
			tgDirectives[v] = tgDirective{field: field, value: v}
		}
	}
	add("type", validTypes)
	add("visibility", validVisibility)
	add("status", validStatus)
}

func tgDirectiveWords() []string {
	words := make([]string, 0, len(tgDirectives))
	for w := range tgDirectives {
		words = append(words, "/"+w)
	}
	sort.Strings(words)
	return words
}

// parseTelegramText 把一条聊天消息变成一次写入请求。
//
//	直接发正文                     → log / private，零语法。这是默认路径。
//	第一行以 / 开头                → 第一行是指令行，正文从第二行开始
//	  /post /public #编译器 标题
//	  正文……
//	第一行以 // 开头                → 逃生舱：正文本来就以 / 开头（贴一段路径、一行命令）
//
// 指令行的读法是「前缀扫描」：从左往右吃 /词 和 #标签，遇到第一个既不是 /词
// 也不是 #标签 的词就停下，那个词到行尾全算标题。所以**标签要写在标题前面**——
// 反过来（标题里带 #）不会被当成标签，这是有意的：正文和标题里出现 # 太常见了
// （`#include`、乐理里的 C#、markdown 标题），把它们悄悄变成标签会写出一批
// 谁也没打算建的 /t/ 页面。
//
// 这里**不填任何默认值**。默认 log / private 由 commitEntry 给。
// 全仓库已经有两处 visibility 默认值（content.config.ts 和 write.go）必须保持一致，
// 第三处只会带来第三种走样的可能。
func parseTelegramText(text string) (writeRequest, error) {
	var req writeRequest
	text = strings.TrimSpace(text)

	switch {
	case strings.HasPrefix(text, "//"):
		req.Text = strings.TrimPrefix(text, "/")
		return req, nil
	case !strings.HasPrefix(text, "/"):
		req.Text = text
		return req, nil
	}

	line, body, _ := strings.Cut(text, "\n")
	fields := strings.Fields(line)
	seen := map[string]string{}

	i := 0
scan:
	for ; i < len(fields); i++ {
		f := fields[i]
		switch {
		case strings.HasPrefix(f, "#"):
			req.Tags = append(req.Tags, strings.TrimPrefix(f, "#"))
		case strings.HasPrefix(f, "/"):
			word := strings.ToLower(strings.TrimPrefix(f, "/"))
			// 群里 Telegram 会把 /post 发成 /post@某个bot。
			if at := strings.IndexByte(word, '@'); at >= 0 {
				word = word[:at]
			}
			d, ok := tgDirectives[word]
			if !ok {
				return req, fmt.Errorf("不认识 %s。可用的指令：%s\n"+
					"正文本来就以 / 开头的话，开头多打一个斜杠",
					f, strings.Join(tgDirectiveWords(), " "))
			}
			// 同一个字段给了两个值时停下来问，而不是「后面的赢」。
			// 手机上没人会回头检查自己发出去的那行指令，而 /public /private 这种手滑
			// 一旦按「后面的赢」处理，结果就是一条本该私密的条目安静地公开着。
			if prev, dup := seen[d.field]; dup && prev != d.value {
				return req, fmt.Errorf("%s 给了两个值：/%s 和 /%s，我不知道你要哪个", d.field, prev, d.value)
			}
			seen[d.field] = d.value
			switch d.field {
			case "type":
				req.Type = d.value
			case "visibility":
				req.Visibility = d.value
			case "status":
				req.Status = d.value
			}
		default:
			break scan
		}
	}

	if i < len(fields) {
		req.Title = strings.Join(fields[i:], " ")
	}
	req.Text = strings.TrimSpace(body)
	if req.Text == "" && req.Title != "" {
		// 只有一行的情况：`/note 一句话`。把它当正文而不是标题——
		// 这条路径上「发一句话」远比「发一个只有标题的空条目」常见，
		// 而 log 型条目本来就没有标题（见 ARCHITECTURE 第 6 节）。
		req.Text, req.Title = req.Title, ""
	}
	return req, nil
}

const tgHelp = `起居注写入通道。

直接发一段文字 → 记一条 log / private。这是默认路径，不用记任何东西。

要改字段就在第一行打指令，空格分开，正文从第二行开始：

/post /public #编译器 写入通道这件事
正文从这里开始。

指令词就是 frontmatter 里的字段值本身：
  类型  /post /note /log /link /list /photo
  可见  /public /unlisted /circle /private
  状态  /seed /growing /done
  标签  #标签（写在标题前面）
  标题  指令后面剩下的部分

只有一行时，那一行是正文，不是标题。
正文本来就以 / 开头（贴路径、贴命令）就多打一个斜杠：//usr/local/bin

改已有条目（scripts/n 的 -i）在这里没有，手机上一个手滑的 id 会覆盖掉一条旧条目。`

// ── HTTP ────────────────────────────────────────────────────────────────────

// telegramRoute 每次调用都新建一份去重缓存，缓存于是和 handler 同生共死，
// 不是包级变量——测试之间因此天然隔离，不会互相看见对方的 update_id。
func (c config) telegramRoute() http.Handler {
	seen := newUpdateCache(updateCacheSize)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.handleTelegram(w, r, seen)
	})
}

// handleTelegram 收 Telegram 的 webhook 推送。
//
// # 状态码是这里最需要想清楚的一件事
//
// Telegram 拿到非 2xx 会重投同一条 update。而这个 handler 会往磁盘写文件，
// 所以「返回什么」不是礼节问题，是「会不会多写一条」的问题。规矩是：
//
//	一旦决定处理这条消息，无论结果如何都回 200，失败在**聊天里**告诉本人。
//
// 反过来做（写失败就回 500 让它重投）看着更「正确」，实际上是把最常见的那类失败
// 变成重投风暴：deploy/README 里排第一的部署坑是 CAIRN_UID 填错导致每一次写入都
// permission denied——那是个永久性错误，重投一万次也还是失败，只会刷满日志。
// 而真正需要重试的人就在手机前面拿着那条消息，一句「没记下：……」比任何自动重试都直接。
//
// 4xx 只留给「我们根本没动手」的情况：secret token 不对（不是 Telegram 发来的）、
// body 解不开、update_id 不合法。这几种情况下重投也没有副作用。
func (c config) handleTelegram(w http.ResponseWriter, r *http.Request, seen *updateCache) {
	// 第一道门。用固定时间比较，和 requireToken 同一个写法、同一个理由。
	// 回 403 不回 401：401 的含义是「换个凭证再来」，而这里没有任何 Authorization
	// 凭证可换——头对不上就说明请求不是 Telegram 发来的，没有下一步。
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(tgSecretHeader)), []byte(c.tg.secret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var up tgUpdate
	dec := json.NewDecoder(r.Body)
	// 故意**不**开 DisallowUnknownFields（/api/write 那边是开的）：Update 这个结构
	// 每个 Bot API 版本都在长新字段，严格模式等于「Telegram 一升级，手机写入就全挂」，
	// 而且挂法是静默的——你以为消息发出去了。这里只挑自己认识的字段，其余一律忽略。
	if err := dec.Decode(&up); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "update 太大", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad update", http.StatusBadRequest)
		return
	}
	// update_id 从一个正数开始递增，不会是 0。要求它是正数不只是校验：
	// 去重完全依赖它，放行 0 等于放行一条永远去不了重的更新。
	if up.UpdateID <= 0 {
		http.Error(w, "bad update", http.StatusBadRequest)
		return
	}

	// 在**处理之前**认领 update_id，不是处理之后。
	// 认领在后的话，Telegram 并发重投（webhook 默认 max_connections 40）会有两个请求
	// 同时穿过去重、同时落盘。认领在前换来的是「至多一次」：极端情况下会漏记一条，
	// 而漏记是看得见的（手机上没收到确认），重记是看不见的。
	if !seen.claim(up.UpdateID) {
		// 不回复。第一次投递时本人已经收到过确认了，再来一句「已记下」是在撒谎。
		log.Printf("telegram：update %d 重复投递，已忽略", up.UpdateID)
		w.WriteHeader(http.StatusOK)
		return
	}

	msg := up.Message
	if msg == nil || msg.From == nil || msg.Chat == nil {
		// 不是我们订阅的更新类型（setWebhook 时限定了 allowed_updates=["message"]，
		// 但远端配置不能当成不变量——它在别人的服务器上，一次手滑就能改）。
		w.WriteHeader(http.StatusOK)
		return
	}

	// 第二道门。
	if !c.tg.allow[msg.From.ID] {
		// 不回复。回复等于向一个陌生人确认「这个 bot 存在，而且它在听」。
		// 这条日志同时是 allowlist 的引导路径：第一次自己给 bot 发消息时，
		// 从日志里抄下这个 id 填进 CAIRN_TELEGRAM_ALLOW。不记消息正文——
		// 陌生人发来的东西没有理由进这台机器的日志。
		log.Printf("telegram：拒绝来自 %d 的消息（不在 allowlist 里）", msg.From.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 只在私聊里工作。allowlist 管的是「谁能写」，而聊天窗口决定的是「谁能看见那句确认」
	// ——确认里可能带着一条 unlisted 的 URL，也就是那条条目的全部秘密。
	// 把它发进一个群，等于把秘密发给群里所有人。所以在群里一律不动作、也不回复。
	if msg.Chat.Type != "private" {
		log.Printf("telegram：忽略来自非私聊（%s）的消息", msg.Chat.Type)
		w.WriteHeader(http.StatusOK)
		return
	}

	c.reply(w, msg.Chat.ID, c.handleTelegramMessage(msg))
}

// handleTelegramMessage 处理一条已经通过两道门的消息，返回要回给本人的那句话。
func (c config) handleTelegramMessage(msg *tgMessage) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		// 图片、语音、转发……都会走到这里（它们没有 text）。
		// 收图要下载文件，那需要 bot token，也就是上面那条「服务端不存 bot token」
		// 的决定要反过来。photo 型条目先留给 scripts/n。
		return "只收纯文本。图片、语音这些还没有。"
	}

	if cmd := tgBareCommand(text); cmd == "help" || cmd == "start" {
		return tgHelp
	}

	req, err := parseTelegramText(text)
	if err != nil {
		return "没记下：" + err.Error()
	}

	resp, err := c.commitEntry(req)
	if err != nil {
		var we *writeError
		if errors.As(err, &we) {
			return "没记下：" + we.msg
		}
		return "没记下：内部错误，去看服务端日志。"
	}

	log.Printf("telegram：%d 写入 %s", msg.From.ID, logID(resp.ID, resp.Visibility))
	return tgConfirmation(resp, c.siteURL)
}

// tgBareCommand 取第一个词里的命令名，去掉开头的 / 和群聊里的 @bot 后缀。
func tgBareCommand(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	fields := strings.Fields(first)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	w := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if at := strings.IndexByte(w, '@'); at >= 0 {
		w = w[:at]
	}
	return w
}

// tgConfirmation 是写成功之后回给本人的那句话。
//
// unlisted 的 id 原样出现在这里是对的：那条链接本来就是本人要来发给别人的，
// 而这是一个只有他自己在的私聊。它不能去的地方是**日志**（见 logID）
// 和 **Telegram 的链接预览抓取**（见 tgSendMessage.LinkPreview）。
// 这里没有「已更新」分支：这条通道只会新建。改一条已有条目要给 id，而 id 一旦手滑
// 就是覆盖——覆盖是整条链路上唯一不可逆的动作，不该放在一块手机键盘后面。
// 要改就在电脑上 `n -i`。
func tgConfirmation(resp writeResponse, siteURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已记下 · %s\n%s", resp.Visibility, resp.ID)
	if resp.URL != "" {
		b.WriteString("\n" + siteURL + resp.URL)
		// 写入是零摩擦的，发布还不是（deploy/README「还没有的东西」第一条）。
		// 不说这一句的话，作者会点开那个链接、看到 404，以为写入失败了。
		b.WriteString("\n（要等下一次 scripts/publish 才会出现在站上）")
	}
	return b.String()
}

// reply 把一次 sendMessage 写进 webhook 的响应体。见文件头的注释。
func (c config) reply(w http.ResponseWriter, chatID int64, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(tgSendMessage{
		Method:      "sendMessage",
		ChatID:      chatID,
		Text:        text,
		LinkPreview: tgLinkPreviewOptions{IsDisabled: true},
	})
}

// ── 去重 ────────────────────────────────────────────────────────────────────

// updateCache 记住最近见过的 update_id，用来挡住 Telegram 的重投。
//
// Telegram 收到非 2xx 会重投同一条 update。上面那条「一律回 200」的规矩已经把绝大多数
// 重投挡在门外了，但它挡不住进程崩溃、也挡不住 Telegram 自己的超时重投——而这条通道
// 一次重投就是磁盘上多一条一模一样的条目，且没人会发现：它看起来就像手滑发了两遍。
//
// 为什么不是「只记最大的 update_id，比它小的一律丢掉」：update_id 确实是递增的，
// 但 webhook 下的**投递顺序**不保证（Telegram 会并发推送，max_connections 默认 40）。
// 用水位线的话，一条只是晚到半秒的正常新消息会被当成重投丢掉——丢的是内容，
// 而且同样没人会发现。所以记一个有界集合，宁可多记 255 条。
//
// 256 条是拍的：这条通道的量级是一天几条，而重投窗口是分钟级，中间隔不出 256 条更新。
// 内存代价是几 KiB，不值得为它引入任何持久化。
//
// 状态在内存里，进程重启就清零，于是「重启前刚投递过的那条又被投一次」会写重。
// 这是有意的取舍：为这个窗口给一个零依赖的服务加一张表（SQLite、文件、什么都算），
// 代价比它挡住的东西大。
type updateCache struct {
	mu   sync.Mutex
	ring []int64
	seen map[int64]struct{}
	next int
}

func newUpdateCache(n int) *updateCache {
	return &updateCache{ring: make([]int64, n), seen: make(map[int64]struct{}, n)}
}

// claim 第一次见到这个 update_id 时记下它并返回 true；见过了返回 false。
func (u *updateCache) claim(id int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, dup := u.seen[id]; dup {
		return false
	}
	if evicted := u.ring[u.next]; evicted != 0 {
		delete(u.seen, evicted)
	}
	u.ring[u.next] = id
	u.next = (u.next + 1) % len(u.ring)
	u.seen[id] = struct{}{}
	return true
}
