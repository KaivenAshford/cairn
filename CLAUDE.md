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
- **`tagSlug` 在两处必须产出一样的结果**(`server/write.go` 与 `web/src/lib/entries.ts`)。
  标签会变成 `/t/<slug>/` 的目录名;两边不一致的后果是「服务端放行、下次构建失败」,
  而构建失败会让 `scripts/publish` 拒绝换产物,整站冻结到有人手改那个 md。
  和「两处默认值都是 private」同一个道理,`server/write_test.go` 有对照断言钉着。
- **纯私密日记不上网。** 站上只放「至少愿意给一个人看」的东西。
- 未实现的东西一律拒绝请求(现在 `/circle/*` 返回 501),绝不为了「先跑起来」而放行。

## 当前状态(2026-09-05)

**基础做扎实了,视觉重做过一轮,出口和 CI 也补齐了。** circle / pipeline / 算力接口
一律停在原地等审核。

**内容与代码分仓**:真实条目在单独的 private 仓库(clone 到 `web/content/entries/`),
代码仓里只有 `web/content/fixtures/` 那 3 条示例。详见 ARCHITECTURE.md 第 2 节。

**视觉是「六色卡纸」**:卡片式时间流,六种 type 各一支色相,卡片**按 type 长得不一样**
(长文最大、短记是一句话、外链带域名、清单铺前几项、影像铺图)。
卡片只有一份实现:`web/src/components/EntryCard.astro`,首页和标签页共用——
它被复制过一次就立刻漂移了(首页加影像分支时标签页没跟上)。

**站现在有出口了**:`/feed.xml`(手写 Atom)、`/sitemap.xml`、`/robots.txt`、
Open Graph、内联 SVG 的 favicon(和站徽同一堆石头)。三样产物的数据源都是
`publicEntries()`——**绝不能是 `loadEntries()`**,那会把 unlisted 的随机 URL
群发给订阅者、交给搜索引擎。

**导航**:标签页 `/t/<slug>/`(标签终于不再是死数据)、条目页的上一篇/下一篇、
首页的年份标记。`/u/` 的未列出页**故意不加上下篇**——那会把别的 unlisted 条目的
URL 泄露给拿到其中一条的人。

**CI**:`.github/workflows/ci.yml`,两个并行 job。注意 CI 上 `web/content/entries/`
不存在(它是另一个仓库),所以 web job 必须先跑 `scripts/seed`。

`server/` 24 个测试(`go test -race` 全过),`scripts/test-visibility` **81 条**性质。
两套都做过变异验证——把修复改坏,确认测试真的会失败。

**未实现,需要先过审再做**:`/circle/*` 的会话与渲染、magic link、`pipeline/` 的数据采集、
算力接口、手机端写入入口、写入后自动重建。

**已知的债**:
- `.yearmark` 的 `position: sticky` 在 grid item 上活动范围只有自己那一行,
  不是注释原本描述的那种「浮在那儿」。要那个效果得先把 `.stream` 从 grid 换掉。
- 条目页用 `.seed`、时间流用 `.status[data-status]`,两套状态标记同时活着。
- `/now/` 与 `/e/now/` 是同一份内容的两个 URL,canonical 各指各的。
  sitemap 只推荐 `/now/`,真要收口得改 `Base.astro`。

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
n -g tsgo,编译器 "正文"                            # 带标签

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
