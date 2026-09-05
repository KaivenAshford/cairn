# cairn · 起居注

石堆路标——每个路过的人放一块石头，日积月累就成了荒野里的地标。

这不是博客，是**一个人的可浏览版本**。文章只是其中一类内容，另外几类是短记、常青笔记、
清单，以及自动采集的个人数据。

中文名取「起居注」：古代逐日记录帝王言行的文体。它自带三层意思，恰好都对得上——
逐日、自动、不加修饰的记录；是档案而非文集；以及那条著名的规矩（皇帝不许看自己的起居注）
所暗示的**分级可见**。

## 核心判断

**成败不在前端，在输入摩擦。** 记录类内容的唯一死因是「记一条要开电脑、进后台、填表单」。
所以写入通道（手机一条消息 / 终端一个命令 → 站上一条记录）和站本身同等重要，
第一版就要有。反过来，先做漂亮的文章页大概率会空着。

## 目录

| 路径 | 作用 |
|---|---|
| `web/` | Astro 静态壳。构建时生成 `public` / `unlisted` 内容 |
| `web/content/entries/` | 真实条目。**单独的 private 仓库**，本目录被 gitignore |
| `web/content/fixtures/` | 几条示例，进代码仓。`scripts/seed` 把它们放进 `entries/` |
| `server/` | Go 服务：写入通道、会话、（未来）算力接口 |
| `scripts/` | `n` 捕获、`seed` 灌示例、`publish` 构建发布、`test-visibility` 性质测试 |
| `pipeline/` | 定时数据采集，产出 JSON 给构建用 |
| `deploy/` | docker compose + Caddy，部署步骤见 [deploy/README.md](deploy/README.md) |

架构决策与理由见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 起步

```sh
cd web && npm install && cd ..
scripts/seed                             # 内容在另一个 private 仓库，本地先灌几条示例
cd web && npm run dev                    # 静态壳，localhost:4321

# 写入通道。没有默认 token：一个能往磁盘写文件的接口不能因为忘配环境变量就裸奔。
cd server && CAIRN_TOKEN=$(openssl rand -hex 32) go run .
```

捕获一条记录（把 `CAIRN_URL` 与 `CAIRN_TOKEN` 写进 `~/.cairnrc`）：

```sh
scripts/n "刚想到的一件事"                 # 默认 log + private
scripts/n -v public -t post -T "标题" "正文"
scripts/n -i now "在建这个站"              # 改写 id 为 now 的那条
```

条目落盘之后要重新构建才会出现在站上：

```sh
scripts/publish                          # 构建到临时目录，成功了才换上去
```

用 `publish` 而不是 `npm run build`：后者会先清空产物目录再生成，而那个目录就是线上
docroot。一次失败的构建（公开目录里混进一条 private 就会失败，那正是设计要的）会把
线上页面全删掉，还会留下含全部条目正文的中间文件。

## 测试

安全性质要有跑得起来的断言，不能靠记得：

```sh
cd server && go test ./...    # 写入通道：默认私有、并发不丢内容、控制字符转义、路径穿越……
scripts/test-visibility       # 构建侧：private 混入公开目录会失败、unlisted 不上首页……
cd web && npm test            # astro check + build
```

给 Claude Code 的项目上下文在 [CLAUDE.md](CLAUDE.md)。
