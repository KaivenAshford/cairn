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
cp deploy/Caddyfile /etc/caddy/Caddyfile && systemctl reload caddy
```

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

**用 `scripts/publish`，不要直接 `npm run build`。** astro build 会先清空 outDir 再生成，
而 outDir 就是 Caddy 的 docroot。一次失败的构建会把线上所有页面删光，并在 docroot 里
留下一批含全部条目正文的中间 chunk。而构建失败是**设计要它发生**的（公开目录里混进一条
private 就该失败），所以这条路径上不能让「构建挂了」升级成「站没了」。

## 还没有的东西

- **写完 public 条目后没有任何东西触发重建。** 现在得手动跑一次 `scripts/publish`。
  写入端是零摩擦的，发布端还不是——这是目前最刺眼的一处不对称。
- **没有 CI。** `go test ./...` 和 `scripts/test-visibility` 都得手动跑。
