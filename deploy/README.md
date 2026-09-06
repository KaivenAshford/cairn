# 部署

形态是**一台机器上两个容器**，服务器上除了 Docker 什么都不用装。

```
caddy 容器（自动 HTTPS）
  ├── /          → /srv/cairn/web/dist      静态产物，绝大多数请求走这里（只读挂载）
  ├── /api/*     → server:8787              写入通道
  └── /circle/*  → server:8787              还没实现，一律 501
server 容器（Go，零依赖）
  └── /content/entries                      内容私有仓，读写挂载
```

**Caddy 从宿主机搬进了 compose。** 从前它是 apt 装 + systemd 管的，理由是「静态产物是
文件，让 Caddy 直接从磁盘发就行，多套一个容器换来的好处是零」。

**那句话当时是对的，现在也还是对的**——它算的是运行时的账，而运行时的账确实是零：
多一个容器、多几处 volume 映射、调试时多一层间接，一样都没少，发文件也没变快。
变的是**要优化的量**。当时的目标是「把它跑起来」，那种目标下「apt install caddy 再写个
systemd unit」是一次性成本，摊在一台机器上等于没有；目标换成**一键部署**之后，要算的
是「一台空白机器到能上线之间有几步人要做」——而 apt 源、systemd unit、`/etc/caddy` 的
属主、各发行版各不相同的包名和路径，是**每台新机器都要重来一遍**的，还恰好是脚本最
难写对、最容易在别人的发行版上崩掉的那一段。搬进 compose 之后，要人做的只剩「装
Docker」。

所以不是「原来的理由错了」，是判断标准换了一道题。完整的理由段落在
[ARCHITECTURE.md 第 5 节](../ARCHITECTURE.md)；代价那几行写在
`docker-compose.yml` 的注释里，和要付代价的那几行放在一起。

同理，**容器里不常驻 npm**。构建在本机或 CI 做完，产物是文件，Caddy 直接发。静态壳没
必要为了一天一次的构建而常驻一个 node 环境。（`scripts/deploy` 在机器上没有可用的 node
时会用 `docker run --rm node:…` 跑一次构建就扔——那是一次性的，不是常驻。）

## 第一次部署

```sh
git clone <代码仓> /srv/cairn
/srv/cairn/scripts/deploy
```

就这两行。脚本会问三个问题（域名、内容私有仓的 git 地址、Telegram bot token——
最后一个可以跳过），其余全部自己干：装 Docker、clone 内容仓并设对属主、
用 `openssl rand -hex 32` 生成 `CAIRN_TOKEN` 和 webhook secret 写进 `.env`（权限 600，
不回显、不进 shell 历史）、换掉 `astro.config.mjs` 里的占位域名、
`npm ci` + `scripts/publish`、`docker compose up`、等证书签下来、`setWebhook`，
最后逐个端点验活。

**真正需要人做、脚本替不了的只有四件：**

1. 买域名，把 A 记录指到这台机器的公网 IP（还要在云厂商的安全组里放行 80 / 443）
2. 找 @BotFather 拿 bot token
3. 建内容私有仓，并给这台机器配好能 clone 它的凭证（deploy key / ssh-agent）
4. 决定域名叫什么

```sh
scripts/deploy --dry-run    # 先看它打算做什么，一个字节都不改
scripts/deploy              # 第一次
scripts/deploy --update     # 之后：拉代码、拉内容、重新构建发布、重启服务、再验一遍
```

**它是幂等的**：`.env` 已存在就一个字不动（里面那个 `CAIRN_TOKEN` 是所有客户端配着的
那个，重新生成不会报错，只会让写入通道和 bot 同时静默失效），内容仓已 clone 就 `pull`，
域名已经换过就跳过。跑第二遍是更新，不是重装。

失败时它**停在原地**并说清楚下一步，不会留下半个部署：`.env` 保留（那是这次生成的密钥，
删了就没了），半截的 clone 会删掉，域名替换会从 `.bak` 还原，`scripts/publish` 本来就是
原子的（构建到 `dist.next`，成功了才 rename，失败时线上产物一个字节没动），
而容器**不会**被停掉——日志是唯一的线索。

### 它做完之后验了什么

「命令返回 0」不算数：`docker compose up` 返回 0 只说明容器创建了，不说明它没在崩溃
循环；Caddy 起来了不说明证书签下来了。所以每一步后面都跟一条断言：

| 断言 | 挂了说明什么 |
|---|---|
| `docker compose exec caddy ls -A /srv/cairn/web/content` 是空的 | 那层遮蔽用的空 tmpfs 没生效，真实条目（含 private）进了 Caddy 那个容器 |
| 首页 200 且真的走了 HTTPS | 证书签下来了、DNS 指对了、静态产物发得出去 |
| `/feed.xml` 200 | 构建产物完整 |
| 猜错的 `/u/<随机>/` → 404，且正文不是 0 字节 | `unlisted` 的「猜不到」有明确的否定兜底；正文为 0 字节说明 `handle_errors` 被删了，站上没有 404 页 |
| `/circle/x` → 501 | 没实现的东西仍然拒绝所有请求 |
| 错 token 打 `/api/write` → 401 | 门是关着的 |
| 真 token 打 `/api/write` → 201，落盘，读回来核对，再删掉 | 写入通道整条链路通了（写的是 `private`，本来也不会上站） |
| `getWebhookInfo` 的 url / `pending_update_count` / `last_error_message` | Telegram 那条通道通了 |

部署之前的那一套检查在 `scripts/preflight`（只读，随便跑）。`scripts/deploy` 在动手之前
会自己调一次，致命项没过就停下——不「先试试看」是有理由的：这些错的症状和原因对不上
（`CAIRN_UID` 填错 → 每条写入 500，80 被占 → 证书签不下来），先部署一遍只会让你去查
错的地方。

## 手动部署（脚本失败时的退路）

**这一节是有意保留的，不是历史遗迹。** 一键脚本会失败——网络断在 `npm ci` 中间、
内容仓的 deploy key 没配好、云厂商的安全组忘了放行 80。失败的那一刻，人需要知道的
不是「脚本挂了」，而是**它本来要做什么、做到哪一步了、剩下的怎么自己接着做**。
一个只有「跑这一行」的文档，在它跑不通的那一天等于没有文档。

脚本干的事没有魔法，就是下面这 6 步（Telegram 那 5 步在下一节）。
每一步旁边标了脚本里对应的小节名，方便对着日志接手。

```sh
# 0. 装 Docker。整个部署只依赖这一件事——服务器上除了 Docker 什么都不用装。   ← 手边的工具
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER   # 然后**重新登录**（newgrp 只对当前这个 shell 生效）

# 1. 两个仓库都要 clone。内容是单独的 private 仓库（见 web/content/README.md），  ← 内容私有仓
#    不 clone 它的话，条目会落进一个空的、不受 git 管理的目录——
#    「服务器炸了不丢」的前提就没了，而且不会有任何报错提示你忘了。
git clone <代码仓> /srv/cairn
git clone <内容仓> /srv/cairn/web/content/entries

# 2. 配置。CAIRN_DOMAIN / CAIRN_TOKEN / CAIRN_UID / CAIRN_GID 缺一个都拒绝启动。  ← .env
cd /srv/cairn && cp .env.example .env && chmod 600 .env && $EDITOR .env
#    CAIRN_TOKEN 和 CAIRN_TELEGRAM_SECRET 都用 `openssl rand -hex 32`；
#    CAIRN_UID / CAIRN_GID 填 `id -u` / `id -g`（就是 clone 内容仓的那个用户）。
#
#    ⚠ 走到这一步之后再回头跑 scripts/deploy 的话，它会看到 .env 里已经有
#      CAIRN_DOMAIN=example.com（.env.example 的出厂值），于是**不再问你域名**
#      ——它的规矩是「.env 里已有的值一个字不改」。结果是它一路走到自检才停下，
#      报三条「还是占位域名」。不算错（fail-closed，停下来了），只是那时候
#      引导式问答已经用不上了。要用问答就别先 cp，直接跑 scripts/deploy。

# 3. 换掉占位域名。只剩 web/astro.config.mjs 的 site 一处要改文本，           ← 占位域名
#    Caddyfile 读的是 {$CAIRN_DOMAIN}，.env 里填一次就够。

# 4. 自检一遍再动手。它只读，随便跑。                                        ← 部署前自检
scripts/preflight

# 5. 构建并发布。用 publish，不要直接 npm run build（理由见「容易踩的几脚」）。 ← 构建并发布
cd web && npm ci && cd .. && scripts/publish

# 6. 起两个容器。--env-file 不是可选的，理由见「容易踩的几脚」。               ← 起容器
docker compose --env-file .env -f deploy/docker-compose.yml up -d --build
```

起完之后把上面「它做完之后验了什么」那张表的断言自己跑一遍——尤其是第一条
（`exec caddy ls -A /srv/cairn/web/content` 必须是空的）和最后两条（401 / 201）。
`docker compose up` 返回 0 只说明容器创建了，不说明它没在崩溃循环。

## Telegram 写入通道

手机上发一条消息 → 站上一条记录。README 的「核心判断」里写的那半件事。
形态是 webhook：Telegram 主动 POST 到 `https://你的域名/api/telegram`。
**得先有域名和证书**，所以这一步排在最后。

服务端**不保存 bot token**：回复是写在 webhook 响应体里的（Telegram 允许在响应里
直接带一次方法调用）。bot token 只用一次——`scripts/deploy` 也一样，它把 token 读进
内存、调完 `setWebhook` 就随进程一起没了，既不写进 `.env`，也不放在命令行上
（写在命令行上的密钥，同机器上任何一个用户 `ps auxww` 都看得见）。
少存一个凭证，就少一个泄露面；而它恰好是唯一一个能冒充这个 bot 的凭证。

`scripts/deploy` 走的就是下面这几步，包括最别扭的那一步——**你不知道自己的数字
user id**。从前的办法是先拿一个假名单把服务起起来、发条消息、去容器日志里 grep
「拒绝来自」。脚本改成在登记 webhook **之前**先 `getUpdates` 认一次（顺序不能反：
webhook 一旦登记，Telegram 就会拒掉 `getUpdates`），认完直接写进 `.env`。

手动的等价步骤：

```sh
# 1. 找 @BotFather 建一个 bot，拿到 bot token（形如 123456:AA...）。
# 2. 生成 webhook 的 secret，填进 .env 的 CAIRN_TELEGRAM_SECRET
openssl rand -hex 32

# 3. 拿到自己的数字 user id（给 @userinfobot 发条消息也行），填进 CAIRN_TELEGRAM_ALLOW。
#    两个都填才开这条通道；只填一个 = 拒绝启动。
docker compose --env-file .env -f deploy/docker-compose.yml up -d

# 4. 把 webhook 登记给 Telegram（BOT_TOKEN 和 SECRET 用上面那两个）
curl -sS "https://api.telegram.org/bot${BOT_TOKEN}/setWebhook" \
  -d "url=https://你的域名/api/telegram" \
  -d "secret_token=${SECRET}" \
  -d 'allowed_updates=["message"]' \
  -d "drop_pending_updates=true"

# 5. 验一遍。pending_update_count 一直涨、last_error_message 有话说，就是没通。
curl -sS "https://api.telegram.org/bot${BOT_TOKEN}/getWebhookInfo"
```

`allowed_updates=["message"]` 不是可选的礼节：不限定的话，Telegram 会把
`edited_message`、`my_chat_member` 之类一起推过来。服务端全都会忽略（那是它的兜底），
但每一条都要走一遍鉴权和去重，白占带宽和日志。

`drop_pending_updates=true` 也别省。在你配 webhook 之前给 bot 发过的消息会攒在
Telegram 那边，一登记就全部涌进来——那些是你还没决定要不要记的东西。

**怎么用**（在私聊里发给 bot）：

```
今天想到的一件事                      → log / private，什么都不用记

/post /public #编译器 写入通道这件事
正文从这里开始。                       → 第一行是指令行，正文从第二行起
```

指令词就是 frontmatter 里的字段值本身（`/post` `/note` `/log` `/link` `/list`
`/photo` / `/public` `/unlisted` `/circle` `/private` / `/seed` `/growing` `/done`），
`#标签` 写在标题前面，标题是指令后面剩下的部分。发 `/help` 会把这些回给你一遍。

**这条通道故意没有 `scripts/n -i`（改已有条目）。** 手机上一个手滑的 id 会覆盖掉一条
旧条目，而覆盖是这条链路上唯一不可逆的动作。要改就在电脑上改。

### 这里容易踩的几脚

**`CAIRN_TELEGRAM_SECRET` 和 `CAIRN_TELEGRAM_ALLOW` 只填一个 → 容器无限重启。**
和 `CAIRN_TOKEN` 留空同一个道理，而且更要紧：这个端点是公网可达的、没有 Bearer 保护。
两个都不填才是「不开这条通道」，那样连路由都不会注册（`/api/telegram` 返回 404）。
（`scripts/deploy` 因此在**写 .env 之前**就把名单认出来——它宁可多问一句，也不写出
一份半配的 `.env`。）

**改了 secret 忘了重新 `setWebhook` → bot 一条也不回，而且不报错。**
Telegram 那边还带着旧 secret，服务端一律 403。`getWebhookInfo` 的
`last_error_message` 会写着 `Wrong response from the webhook: 403 Forbidden`。
改完 secret 跑 `scripts/deploy --telegram` 重新登记一次。

**发进群里的消息不会被记，也不会有任何回应。** 这是有意的：确认里可能带着一条
`unlisted` 的链接，那是那条条目的全部秘密，发进群等于发给群里所有人。

**去重状态在内存里，容器重启就清零。** 重启前后各收到同一条重投会写重一条。
换来的是这个服务仍然零依赖、零持久化状态。

**演练部署（`CAIRN_DOMAIN=localhost`）上配不了 Telegram。** webhook 要求公网可达
且证书公开可信，内置 CA 签的那张它不认。`scripts/deploy` 会直接跳过并说明。

## 容易踩的几脚

下面这些现在**大部分都有断言兜着**了（`scripts/preflight` 查配置，`scripts/deploy`
查跑起来之后的行为）。留在这里是因为断言告诉你「哪儿不对」，而这一节告诉你「为什么」。

**忘了 `--env-file .env` → 报「没配 CAIRN_DOMAIN」，而 .env 里明明填着。**
compose 的变量替换读的是**项目目录**下的 `.env`，而项目目录默认是 compose 文件所在的
`deploy/`，不是仓库根。所以每一条 compose 命令都得写成：

```sh
docker compose --env-file .env -f deploy/docker-compose.yml <子命令>
```

**`CAIRN_UID` 填错 → 写入通道对每一条都返回 500。** 镜像默认以 nonroot（uid 65532）跑，
而那个 volume 是宿主机上的普通目录，属于 clone 仓库的那个用户。uid 对不上就写不进去，
而响应里只有一句 `internal error`，真正的原因（`permission denied`）只在容器日志里。

**`CAIRN_TOKEN` 留空 → 容器无限重启。** 服务拒绝在没有 token 的情况下启动（一个能往磁盘
写文件的接口不能因为忘配环境变量就裸奔），配上 `restart: unless-stopped` 就是崩溃循环，
healthcheck 一直红。`docker compose logs server` 第一行就会告诉你。
`CAIRN_DOMAIN` / `CAIRN_UID` / `CAIRN_GID` 是同一类，只是它们在 compose 里写成
`${VAR:?…}`，连容器都建不出来——那反而是好事，错误信息就在眼前。

**shell 里 export 着的同名变量会盖掉 `.env`，而 `.env` 看着好好的。**
compose 有两条取值路径，它们的优先级是反的：**变量插值**（compose 文件里那些
`${VAR}`）取「shell 环境 > `--env-file`」，而 `env_file:` 是直接灌进容器；两者撞在
`environment:` 段上时，`environment:` 赢。于是 compose 里那行
`TZ: ${TZ:-Asia/Shanghai}`，在一个 `export TZ=…` 过的 shell 里拿的是 shell 那个值，
`.env` 里写的那行**一点用都没有**。实测（compose v2.39.1）：`.env` 写 `TZ=Asia/Tokyo`、
shell 里 `TZ=America/Los_Angeles`，`docker compose config` 解析出来的是后者。
症状：条目的 `created` 和 id 里的日期前缀按另一个时区算，晚上写的东西标成前一天；
换成 `CAIRN_DOMAIN` 就是 Caddy 去给另一个域名签证书，而 `.env` 里明明写着对的。
`scripts/preflight` 现在会把这种「被 shell 盖住」的变量点名报出来。

**别在 compose 里删掉 `CAIRN_ADDR: 0.0.0.0:8787`。** `.env` 里那个 `127.0.0.1:8787`
是给宿主机直跑准备的；容器里绑回环的话，Caddy（另一个容器）一条也连不进来，
而症状极具迷惑性——容器 running、healthcheck 绿的（探活也在容器内），只有 `/api/*` 全线 502。

**8787 没有端口映射，是有意的。** Caddy 和它同在一个 compose 网络里，用服务名就够。
留着 `127.0.0.1:8787:8787` 的话，一个能往磁盘写文件、只靠一个 Bearer token 把门的端点，
对宿主机上每一个本地进程、每一个 SSH 上来的用户都可达。代价是不能直接在宿主机上 curl
它，换成：

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec caddy wget -qO- http://server:8787/health
docker compose --env-file .env -f deploy/docker-compose.yml exec server /cairn-server -healthcheck
```

**Caddyfile 里的 `reverse_proxy` 指的是 `server:8787`，不是 `127.0.0.1:8787`。**
Caddy 自己也在容器里，它的回环就是它自己——写 127.0.0.1 是反代到自己身上，
而症状是 502，和「Go 服务没起来」一模一样。

**改 Caddyfile 时别把 `handle_errors` 那段删了 —— 删掉 = 站上没有 404 页。**
`file_server` 找不到文件时返回的是 Caddy 自带的空白 404（实测 0 字节，状态码仍是 404），
它**不会**自己去用同目录下的 `404.html`；`web/src/pages/404.astro` 构建出来的那一页要靠
`handle_errors` 才发得出去。里面两处看着可以省、其实不能省：

- **按状态码分流。** 只有 404 才发那一页。Go 服务没起来时 `reverse_proxy` 报的是 502，
  给 502 回一张「没有这一页」等于把「服务挂了」说成「你地址输错了」，
  而这两件事该做的处置完全相反。
- **`file_server { status 404 }`。** 发一个存在的文件默认是 200，于是「找不到」会以 200
  发出去（soft 404）。搜索引擎会把这张页当正文收录；更要紧的是 `unlisted` 那套
  「猜不到」的防护，前提正是猜错时得到一个明确的否定。

`/api/*` 和 `/circle/*` 不受影响：`reverse_proxy` 原样透传上游的状态码，
Go 服务自己返回的 401 / 501 是正常响应，不是错误，不进错误路由。

**`CAIRN_DOMAIN` 里的通配符不能放在第一个。** 整串是给 Caddy 签证书用的，
`*.example.org` 在那里完全合法；但**第一个**那个还要被拿去做三件别的事——写进
`CAIRN_URL`、换进 `web/astro.config.mjs` 的 `site`、以及验活时 curl 它。
`https://*.example.org/` 谁也打不开。这一脚特别阴：构建**不会**失败（实测照样出整站），
只是每一页的 canonical、`og:url`、sitemap 的 `<loc>` 都指向一个不存在的主机；
真正停下来的地方是三分钟后的「首页拿不到 200」，而那句话给的原因是
「证书签不下来 / A 记录没指过来」——又一次症状和原因对不上。
要通配符就把具体域名放前面：`--domain 'cairn.example.org *.cairn.example.org'`。
`scripts/deploy` 和 `scripts/preflight` 现在都会拦。

**证书那两个具名卷（`caddy_data` / `caddy_config`）不要删。** `docker compose down -v`
的 `-v` 会连它们一起清掉，于是下一次 `up` 是一次全新签发——而 Let's Encrypt 对「同一组
域名的重复证书」是每周 5 张，试几次就被锁到下周。症状是「网站打不开 / 证书错误」，
谁也不会把它和三天前多重建了几次容器联系起来。要反复演练部署，把 `CAIRN_DOMAIN` 设成
`localhost`：Caddy 会用内置 CA 签，完全不碰 ACME，也就不消耗任何配额。

**用 `scripts/publish`，不要直接 `npm run build`。** astro build 会先清空 outDir 再生成，
而 outDir 就是 Caddy 的 docroot。一次失败的构建会把线上所有页面删光，并在 docroot 里
留下一批含全部条目正文的中间 chunk。而构建失败是**设计要它发生**的（公开目录里混进一条
private 就该失败），所以这条路径上不能让「构建挂了」升级成「站没了」。

## 还没有的东西

- **写完 public 条目后没有任何东西触发重建。** 现在得手动跑一次 `scripts/publish`
  （或者 `scripts/deploy --update`）。写入端是零摩擦的（终端 `scripts/n`、手机 Telegram
  都是），发布端还不是——这是目前最刺眼的一处不对称，而且多一条写入通道就更刺眼一点。
- **没有回滚。** `scripts/deploy --update` 拉了新代码、构建挂了的话，线上产物还是旧的
  （publish 是原子的），但代码仓已经在新提交上了。要退回去只能自己 `git -C /srv/cairn
  checkout <旧的>` 再跑一次。
