# cairn · 起居注

**这不是博客,是「一个人的可浏览版本」。** 文章只是其中一类内容,另外还有短记、常青笔记、
清单,以及自动采集的个人数据。名字取自荒野石堆路标——一次放一块石头,日积月累成地标。

## 先读这个

**所有架构决策和它们的理由在 [ARCHITECTURE.md](ARCHITECTURE.md),动设计前先读。**
下面几条是已经拍板的,不需要重新讨论:

1. **静态优先,按路由开动态。** 判断标准只有一条:这个页面的内容对每个访客是不是一样的?
   一样 → 构建时生成;不一样(个性化/写入/实时/要算力)→ 走服务端。
2. **可见度四层**:`public` / `unlisted` / `circle` / `private`。
   `circle` 和 `private` **根本不进静态构建产物**,由服务端现渲染。
   前端 `if (!loggedIn) return null` 等于零防护——字节已经下发了。
3. **认证用 allowlist + magic link,不做用户系统。** 场景是「我知道我想给谁看」。
4. **内容模型只有一种「条目」**,用正交属性(`type`/`visibility`/`status`)描述。
   加一种新内容类型 = 多一个 `type` 值,架构不动。
5. **成败不在前端,在输入摩擦。** 记录类内容的唯一死因是「记一条要开电脑、进后台、填表单」。
   所以写入通道和站本身同等重要。

## 硬约束

- **两处默认值都是 `private`**(`web/src/content.config.ts` 与 `server/write.go`)。
  忘写 `visibility` 时应当「消失」而不是「泄露」。要公开必须显式声明。
- **`circle`/`private` 条目不进静态产物**,由 `publicEntries()` / `unlistedEntries()`
  显式挑出该发的那些。这是条软防线,所以 `scripts/test-visibility` 第 1、2 组断言它们的
  标题、正文、标签、id 都不出现在 `dist` 的**任何**文件里——包括构建中间产物。
- **`unlisted` 的文件名就是 URL**,必须是长随机串;它的全部防护就是猜不到。
  三处都拦:服务端自动生成 32 位随机名、服务端拒绝「显式 id + unlisted」的组合、
  构建侧(`web/src/lib/entries.ts`)检查 id 里有没有一段随机串。少任何一处都会出现
  「写入回 201,构建却整站失败」或者「一声不吭生成 /u/secret/」。
- **更新条目时忘写 `visibility` 保持原样,不掉回 private。** 默认 private 防的是新建时
  漏字段导致泄露;把一条已公开的条目改成私有是数据丢失,不是安全。两个默认值同一条原则:
  不因为漏写字段而改变可见度。
- **内容不属于这个仓库。** 真实条目在单独的 private 仓库里,clone 到 `web/content/entries/`
  (已 gitignore);代码仓里只有 `web/content/fixtures/` 那几条示例,`scripts/seed` 灌进去。
  好处是消灭了唯一一类不可逆的泄露(私密条目进 git 历史改不掉),代价是「谁进构建产物」
  从目录分离(硬)变成了字段过滤(软),必须靠 `scripts/test-visibility` 钉住。
- **卡片按 type 分叉是设计的前提,不是装饰。** chip 的 `data-type` 少了,六种颜色全部
  退回默认色,整版设计只剩白卡;清单卡不铺项、外链卡不显示域名的话,一张「在读」和
  一条随手记在屏幕上一样重。这几条都由 `scripts/test-visibility` 钉着。
- **纯私密日记不上网。** 站上只放「至少愿意给一个人看」的东西。
- 未实现的东西一律拒绝请求(现在 `/circle/*` 返回 501),绝不为了「先跑起来」而放行。

## 当前状态(2026-09-05)

**基础(博客那一半)做扎实了,视觉也重做过一轮。** 其余部分一律停在原地等审核。

**内容与代码已分仓**:真实条目在单独的 private 仓库(clone 到 `web/content/entries/`),
代码仓里只有 `web/content/fixtures/` 那 3 条示例。服务端因此只有一个内容目录,
可见度纯由 frontmatter 决定。详见 ARCHITECTURE.md 第 2 节。

**视觉是「六色卡纸」**:卡片式时间流,六种 type 各一支色相(OKLCH 同明度带上取,
锁死明度只转色相,六个色块份量才一样重),每张卡是自己那支淡色底 + 顶端渐变色条 +
实心 chip。而且卡片**按 type 长得不一样**——长文最大、短记是一句话、外链带域名、
清单直接铺前几项。这一版之前有过五个方案,选它的过程记在 `audit/`。

`server/`(Go,零依赖)`gofmt` / `go vet` / `go test -race` 全过,26 个测试。
`web/`(Astro 5)`astro check` 0 errors,`scripts/test-visibility` 26 条性质全过。
两套测试都做过变异验证(把修复改坏,确认测试真的会失败),不是摆设。

**这一轮修掉的真问题**(每条都有对应测试兜住):

| 问题 | 后果 |
|---|---|
| `os.Stat` + `os.Rename` 的 TOCTOU | 12 路并发同标题 → 9 个 201 但磁盘只剩 1 个文件 |
| `yamlString` 不转义 C0 字符 | 带 ANSI 码的标题 → 落盘成功 → 下次构建整站挂 |
| `findEntry` 丢掉 `parseFrontmatter` 的 ok | 更新 CRLF/BOM 行尾的公开条目 → 200 但字段全被抹掉、掉回 private |
| 显式 id 绕过 unlisted 随机名 | 回 201 + 一个永不存在的 URL,而整站从此构建不出来 |
| upsert 读-改-写无串行化 | 并发时「收回成 private」被沿用旧值的编辑写回 public |
| `formatDate` 用本地时区 | UTC 以西每一条都显示成前一天 |
| `astro build` 直写 docroot | 构建失败先清空产物,还留下含全部正文的中间 chunk |
| 容器 uid 与 mount 属主不符 | 按 `deploy/` 起容器,写入通道每条都 500 |
| `.stream` 的 grid 轨道没有 0 下限 | 一条长 URL 撑破视口,200% 字号下横向滚动(WCAG 1.4.4) |
| 碎片卡标题可能整个为空 | 纯表格/纯代码块的条目 → 一张没有任何文字、却整片可点的卡 |
| `displayTitle` 只看正文首行 | 正文以表格开头 → 标题退回「（无题）」,而下一段明明有正文 |
| `listItems` 放行缩进子项 | 子项被当顶层铺出来,还挤占名额让「还有 N 条」算错 |
| `linkHost` 没排除图片语法 | 外链卡显示的是配图的图床域名,不是链接真正指向的地方 |
| 筛选脚本抓所有 `li` | 按类型筛选会把清单卡自己的预览项一起隐藏 |
| 用 `opacity` 表达弱化 | 它会乘到前景色上,两处因此掉到 AA 以下(3.57 / 3.96) |
| `test-visibility` 无条件覆盖 fixture | **会静默删掉内容目录里的同名真实条目**,而那个目录不进代码仓的 git |

**新增能力**:写入通道支持按 id 更新(`n -i now "..."`),保留 `created`、写 `updated`、
继承没提供的字段。

**未实现,且需要先过审再做**:`/circle/*` 的会话与渲染、magic link、`pipeline/` 的数据采集、
算力接口、手机端写入入口、写入后自动重建、CI。

**还欠两处**(都在 `audit/` 的验证报告里):
1. `photo` 是六型里唯一拿到了颜色却没有结构分支的——影像卡上从不出现图像。
   要么做图片支持,要么把注释改成「四种有分支」。
2. 暗色代码块的注释色只有 3.05:1(github-dark 自带),改 CSS 修不掉,
   要换成 `*-high-contrast` 主题,代价是配色变艳一点。

## 下一步(按这个顺序)

1. 第一个 commit
2. 建内容私有仓库,把 `web/content/entries/` 指过去(见 web/content/README.md)
3. 部署:域名 + Caddy + compose,步骤见 [deploy/README.md](deploy/README.md);
   三处占位域名要一起换(`deploy/Caddyfile`、`web/astro.config.mjs`、`.env` 的 `CAIRN_URL`)
4. 开始**用**它——现在站上 3 条条目全是在讲这个站自己
5. 以下都要先跟作者确认再动:数据模块(`/ai`)、手机端入口、circle/magic link、自动重建与 CI

## 常用命令

```sh
cd web && npm install && cd ..
scripts/seed                                      # 内容在另一个私有仓库,本地先灌示例
cd web && npm run dev                             # 静态壳,localhost:4321
cd server && CAIRN_TOKEN=$(openssl rand -hex 32) go run .

n "刚想到的一件事"                                  # scripts/n,默认 log + private
n -v public -t post -T "标题" "正文"
n -i now "在建这个站"                               # 按 id 更新已有条目

scripts/publish                                   # 构建并发布(不要直接 npm run build)

cd server && go test ./...                        # 写入通道的安全性质
scripts/test-visibility                           # 构建侧的可见度性质
cd web && npm test                                # astro check + build
```

改了 `server/write.go` 或 `web/src/lib/entries.ts` 一定要跑那两组测试——里面每一条都是
安全性质,不是功能性质。「标了 private 却被构建进公开产物」不会有人报 bug,
它安静地发生,然后一直公开着。

## 写作风格

仓库里的文档记的是**为什么这么选**,不是**选了什么**。改设计时同步更新 ARCHITECTURE.md 的
理由段落;只改结论不改理由,下一个人(或下一个 session)就会把同样的弯路再走一遍。
