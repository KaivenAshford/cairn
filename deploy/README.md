# 部署

形态是**一台机器上两个东西**：宿主机上的 Caddy，加一个只跑 Go 服务的容器。

```
Caddy（宿主机原生安装，自动 HTTPS）
  ├── /          → /srv/cairn/web/dist      静态产物，绝大多数请求走这里
  ├── /api/*     → 127.0.0.1:8787           写入通道
  └── /circle/*  → 127.0.0.1:8787           还没实现，一律 501
```

**Caddy 不在 compose 里，是有意的。** 静态产物是文件，让 Caddy 直接从磁盘发就行；
把它也塞进 compose 意味着多一个容器、多一份 volume 映射、多一层调试时的间接，
而换来的好处是零。容器里只有那个必须常驻的进程：Go 服务。

同理，**容器里不跑 npm**。构建在本机或 CI 做完，产物是文件，Caddy 直接发。
静态壳没必要为了一天一次的构建而常驻一个 node 环境。

## 第一次部署

```sh
# 1. 两个仓库都要 clone。内容是单独的 private 仓库（见 web/content/README.md），
#    不 clone 它的话，条目会落进一个空的、不受 git 管理的目录——
#    「服务器炸了不丢」的前提就没了，而且不会有任何报错提示你忘了。
git clone <代码仓> /srv/cairn
git clone <内容仓> /srv/cairn/web/content/entries

# 2. 配置
cd /srv/cairn
cp .env.example .env
$EDITOR .env          # CAIRN_TOKEN 必填（openssl rand -hex 32）
                      # CAIRN_UID / CAIRN_GID 填 `id -u` / `id -g`

# 3. 换掉三处占位域名
#    deploy/Caddyfile 的 example.com、web/astro.config.mjs 的 site、.env 的 CAIRN_URL

# 4. 构建并发布静态产物
cd web && npm ci && cd ..
scripts/publish

# 5. 起写入服务
docker compose -f deploy/docker-compose.yml up -d

# 6. Caddy
cp deploy/Caddyfile /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile   # 先验一遍，错在哪它会直说
systemctl reload caddy
```

## Telegram 写入通道

手机上发一条消息 → 站上一条记录。README 的「核心判断」里写的那半件事。
形态是 webhook：Telegram 主动 POST 到 `https://你的域名/api/telegram`。
**得先有域名和证书**，所以这一步排在上面那六步之后。

服务端**不保存 bot token**：回复是写在 webhook 响应体里的（Telegram 允许在响应里
直接带一次方法调用）。bot token 只在你自己机器上用一次——就是下面这两条 curl。
少存一个凭证，就少一个泄露面；而它恰好是唯一一个能冒充这个 bot 的凭证。

```sh
# 1. 找 @BotFather 建一个 bot，拿到 bot token（形如 123456:AA...）。
#    顺手关掉群里的隐私模式没必要——这个 bot 只在私聊里工作。

# 2. 生成 webhook 的 secret，填进 .env 的 CAIRN_TELEGRAM_SECRET
openssl rand -hex 32

# 3. 先拿一个占位的名单把服务起起来，为的是从日志里读到自己的 user id
#    .env: CAIRN_TELEGRAM_ALLOW=1
docker compose -f deploy/docker-compose.yml up -d
#    给 bot 发一条「hi」，然后：
docker compose -f deploy/docker-compose.yml logs server | grep 拒绝来自
#    → telegram：拒绝来自 12345678 的消息（不在 allowlist 里）
#    把 12345678 填进 CAIRN_TELEGRAM_ALLOW，重启容器。

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

**改了 secret 忘了重新 `setWebhook` → bot 一条也不回，而且不报错。**
Telegram 那边还带着旧 secret，服务端一律 403。`getWebhookInfo` 的
`last_error_message` 会写着 `Wrong response from the webhook: 403 Forbidden`。

**发进群里的消息不会被记，也不会有任何回应。** 这是有意的：确认里可能带着一条
`unlisted` 的链接，那是那条条目的全部秘密，发进群等于发给群里所有人。

**去重状态在内存里，容器重启就清零。** 重启前后各收到同一条重投会写重一条。
换来的是这个服务仍然零依赖、零持久化状态。

## 容易踩的几脚

**`CAIRN_UID` 填错 → 写入通道对每一条都返回 500。** 镜像默认以 nonroot（uid 65532）跑，
而那个 volume 是宿主机上的普通目录，属于 clone 仓库的那个用户。uid 对不上就写不进去，
而响应里只有一句 `internal error`，真正的原因（`permission denied`）只在容器日志里。

**`CAIRN_TOKEN` 留空 → 容器无限重启。** 服务拒绝在没有 token 的情况下启动（一个能往磁盘
写文件的接口不能因为忘配环境变量就裸奔），配上 `restart: unless-stopped` 就是崩溃循环，
healthcheck 一直红。`docker compose logs server` 第一行就会告诉你。

**别在 compose 里删掉 `CAIRN_ADDR: 0.0.0.0:8787`。** `.env` 里那个 `127.0.0.1:8787`
是给宿主机直跑准备的；容器里绑回环的话，经端口映射进来的连接会被全部拒绝，
而症状极具迷惑性——容器 running、healthcheck 绿的（探活也在容器内），只有外部访问失败。

**改 Caddyfile 时别把 `handle_errors` 那段删了 —— 删掉 = 站上没有 404 页。**
`file_server` 找不到文件时返回的是 Caddy 自带的空白 404，它**不会**自己去用同目录下的
`404.html`；`web/src/pages/404.astro` 构建出来的那一页要靠 `handle_errors` 才发得出去。
里面两处看着可以省、其实不能省：

- **按状态码分流。** 只有 404 才发那一页。Go 服务没起来时 `reverse_proxy` 报的是 502，
  给 502 回一张「没有这一页」等于把「服务挂了」说成「你地址输错了」，
  而这两件事该做的处置完全相反。
- **`file_server { status 404 }`。** 发一个存在的文件默认是 200，于是「找不到」会以 200
  发出去（soft 404）。搜索引擎会把这张页当正文收录；更要紧的是 `unlisted` 那套
  「猜不到」的防护，前提正是猜错时得到一个明确的否定。

`/api/*` 和 `/circle/*` 不受影响：`reverse_proxy` 原样透传上游的状态码，
Go 服务自己返回的 401 / 501 是正常响应，不是错误，不进错误路由。

**用 `scripts/publish`，不要直接 `npm run build`。** astro build 会先清空 outDir 再生成，
而 outDir 就是 Caddy 的 docroot。一次失败的构建会把线上所有页面删光，并在 docroot 里
留下一批含全部条目正文的中间 chunk。而构建失败是**设计要它发生**的（公开目录里混进一条
private 就该失败），所以这条路径上不能让「构建挂了」升级成「站没了」。

## 还没有的东西

- **写完 public 条目后没有任何东西触发重建。** 现在得手动跑一次 `scripts/publish`。
  写入端是零摩擦的（终端 `scripts/n`、手机 Telegram 都是），发布端还不是
  ——这是目前最刺眼的一处不对称，而且多一条写入通道就更刺眼一点。
- **没有 CI。** `go test ./...` 和 `scripts/test-visibility` 都得手动跑。
