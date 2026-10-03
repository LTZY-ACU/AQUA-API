# LTZY-API 部署指南

> 适用版本：v1.0.0 及以上 · 更新日期：2026-10-03
>
> LTZY-API 是**单二进制**程序（前端已通过 `go:embed` 打进可执行文件，SQLite 零外部依赖、
> 不需要 CGO），因此部署只需要三样东西：**一个二进制 + 一个环境变量文件 + 一条 systemd 单元**。
> 没有数据库要装、没有 Node 要装、没有前端要单独托管。

- [一、部署方式怎么选](#一部署方式怎么选)
- [二、方式一：Docker Compose（最快）](#二方式一docker-compose最快)
- [三、方式二：Linux systemd + 二进制（生产推荐）](#三方式二linux-systemd--二进制生产推荐)
- [四、方式三：Windows 裸跑（体验/开发）](#四方式三windows-裸跑体验开发)
- [五、首次启动：安装向导与管理员](#五首次启动安装向导与管理员)
- [六、反向代理与 HTTPS](#六反向代理与-https)
- [七、升级与回滚](#七升级与回滚)
- [八、CLI 子命令速查](#八cli-子命令速查)
- [九、环境变量速查](#九环境变量速查)
- [十、常见问题](#十常见问题)

---

## 一、部署方式怎么选

| 场景 | 推荐方式 | 原因 |
| --- | --- | --- |
| 快速体验 / 小站自用 | Docker Compose | 三条命令起服务，数据落在宿主机 `./data` |
| 对外运营 / 长期生产 | systemd + 二进制 | 官方生产同款，最小权限加固，日志走 journald |
| Windows 本机试用 | 裸跑 | 双击即用，适合先看一眼再决定 |

> 三种方式的**数据完全同构**（都只是一个 SQLite 文件），随时可互相迁移——
> 打包 `aqua.db` + 备份 `AQUA_APP_KEY` 即可整机搬家。

---

## 二、方式一：Docker Compose（最快）

```bash
git clone https://github.com/LTZY-ACU/LTZY-API.git
cd LTZY-API

# 1) 准备环境变量（唯一必填项是加密主密钥）
cp .env.example .env
docker run --rm ltzy-api:local -gen-key > /dev/null 2>&1 || \
  ./aqua -gen-key          # 若本机无二进制，先用下一步构建出的镜像生成

# 2) 构建并启动（首次构建约 1-3 分钟）
docker compose up -d --build

# 3) 打开站点
#    http://127.0.0.1:8787
```

`.env` 里把 `AQUA_APP_KEY` 填成上一步生成的值即可启动。
容器带健康检查（`/healthz`），数据落在宿主机 `./data` 目录——**不是匿名卷**，
迁移服务器时直接打包该目录。

---

## 三、方式二：Linux systemd + 二进制（生产推荐）

> 仓库根目录自带 [aqua-api.service](aqua-api.service) 单元文件（含最小权限加固），
> 以下步骤与它保持一致，可对照使用。

### 3.1 获取二进制

**途径 A：直接下载官方 Release（推荐）**

到 [Releases](https://github.com/LTZY-ACU/LTZY-API/releases) 下载
`aqua-linux-amd64`（Linux）或 `aqua-windows-amd64.exe`（Windows），重命名为 `aqua`。

**途径 B：自行构建（本机无 gcc 也行，必须 CGO_ENABLED=0）**

```bash
git clone https://github.com/LTZY-ACU/LTZY-API.git && cd LTZY-API

# 1) 构建前端（dist 会被 go:embed 打进二进制，必须先做）
cd web && npm install && npm run build && cd ..

# 2) 注入版本信息并交叉编译
VER=v1.0.0
COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
go build -trimpath -ldflags "-s -w \
  -X github.com/LTZY-ACU/ltzy-api/internal/version.Version=$VER \
  -X github.com/LTZY-ACU/ltzy-api/internal/version.GitCommit=$COMMIT \
  -X github.com/LTZY-ACU/ltzy-api/internal/version.BuildTime=$BUILD_TIME" \
  -o aqua ./cmd/ltzy
```

> 版本注入后 `./aqua -version`、`/healthz`、页脚都会显示真实版本号；
> 不注入则显示 `dev`——功能完全一致，只影响排障时「跑的是哪一版」的判断。

### 3.2 安装为 systemd 服务

```bash
# 1) 专用系统用户与目录
sudo useradd -r -s /usr/sbin/nologin aqua
sudo mkdir -p /opt/aqua /etc/aqua /var/lib/aqua

# 2) 放置二进制
sudo cp aqua /opt/aqua/aqua && sudo chown aqua:aqua /opt/aqua/aqua

# 3) 环境变量文件（权限 0600，只给 root 读）
sudo cp .env /etc/aqua/aqua.env
sudo chmod 600 /etc/aqua/aqua.env

# 4) 注册服务
sudo cp aqua-api.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now aqua-api
```

### 3.3 验证

```bash
systemctl status aqua-api          # 应为 active (running)
curl http://127.0.0.1:8787/healthz # 期望 {"status":"ok",...}
```

---

## 四、方式三：Windows 裸跑（体验/开发）

```powershell
# 1) 生成加密主密钥
.\aqua.exe -gen-key
# 复制输出的密钥

# 2) 注入主密钥（当前窗口）
$env:AQUA_APP_KEY = "粘贴密钥"

# 3) 启动（数据落在当前目录 aqua.db）
.\aqua.exe
# 浏览器打开 http://127.0.0.1:8787
```

---

## 五、首次启动：安装向导与管理员

首次访问首页时，站点会自动引导进入**安装向导**（检测到数据库为空时开放）：

1. 浏览器打开 `http://<你的地址>:8787/`；
2. 按向导设置站点名称、管理员账号与密码；
3. 完成后向导自动关闭，此后只能通过管理员后台或 CLI 修改。

> 偏好命令行的同学可以用 CLI 直接创建管理员（跳过向导）：
>
> ```bash
> ./aqua -init-admin <用户名>    # 随机密码只打印一次
> ./aqua -init-admin admin -new-password '你的强口令'   # 指定密码则不回显明文
> ```

创建完管理员后，第一件建议做的事：
**进后台「渠道管理」添加上游渠道 → 「计价规则」定价 → 「令牌管理」发一个令牌**，
然后用任意 OpenAI 兼容客户端把 Base URL 指向 `http://<你的地址>:8787/v1` 即可开始使用。

---

## 六、反向代理与 HTTPS

生产环境强烈建议：**程序只听 127.0.0.1，HTTPS 与域名交给反代处理**。

### 6.1 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name api.example.com;

    ssl_certificate     /etc/letsencrypt/live/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.example.com/privkey.pem;

    # 网关必须关闭缓冲：SSE 流式响应一旦被缓冲，客户端就是"等半天一口气吐完"
    proxy_buffering off;
    # 流式对话可能持续很久，默认 60s 会把长回答掐断
    proxy_read_timeout  300s;

    client_max_body_size 50m;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

对应地，`/etc/aqua/aqua.env` 里监听地址保持：

```ini
AQUA_SERVER_LISTEN=127.0.0.1:8787
```

### 6.2 Caddy（自动申请 HTTPS，两行）

```caddy
api.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

---

## 七、升级与回滚

### 升级（三条命令）

```bash
sudo systemctl stop aqua-api
sudo cp ./aqua-new /opt/aqua/aqua        # 替换二进制
sudo systemctl start aqua-api
```

数据库结构升级在启动时**自动完成**（版本化迁移，无需手工执行 SQL）。

### 回滚

```bash
# 1) 换回旧二进制（升级前先备份总是好习惯）
sudo cp /opt/aqua/aqua.bak-<时间戳> /opt/aqua/aqua
sudo systemctl restart aqua-api
```

> 迁移（schema）只前进不后退：**新版跑过的库不要再用旧版二进制起服务**。
> 回滚前请先用新版二进制产生的数据库备份覆盖回去。

### 必须备份的两样东西

| 备份对象 | 位置示例 | 丢了会怎样 |
| --- | --- | --- |
| 数据库 | `/var/lib/aqua/data/aqua.db` | 全部用户、渠道、账目丢失 |
| 加密主密钥 | `/etc/aqua/aqua.env` 里的 `AQUA_APP_KEY` | **渠道密钥全部无法解密**（无法用旧库直接换新密钥） |

建议把两者一起定时打包（`sqlite3 aqua.db ".backup backup.db"` 在线备份不锁库）。

---

## 八、CLI 子命令速查

| 命令 | 用途 |
| --- | --- |
| `./aqua -version` | 打印版本、提交哈希、构建时间 |
| `./aqua -gen-key` | 生成加密主密钥（部署第一步） |
| `./aqua -init-admin <用户名>` | 创建管理员（随机密码仅打印一次） |
| `./aqua -reset-password <用户名>` | 重置密码；配合 `-new-password` 指定新口令 |
| `./aqua -create-token <名称>` | 创建一个访问令牌（自动化场景） |
| `./aqua -config <路径>` | 指定配置文件（不存在则用默认值 + 环境变量） |

---

## 九、环境变量速查

完整样例见 [.env.example](.env.example)。**密钥类只认环境变量**，写进配置文件会被忽略。

| 变量 | 必填 | 默认 | 说明 |
| --- | :-: | --- | --- |
| `AQUA_APP_KEY` | ✅ | — | 加密主密钥（`-gen-key` 生成）；换了它，已存渠道密钥解不开 |
| `AQUA_SERVER_LISTEN` | | `127.0.0.1:8787` | 容器内必须 `0.0.0.0`；裸机保持 127.0.0.1 交给反代 |
| `AQUA_SERVER_MODE` | | `release` | `debug` 会输出更详细日志 |
| `AQUA_DATABASE_DRIVER` | | `sqlite` | 当前仅 SQLite |
| `AQUA_DATABASE_DSN` | | `/data/aqua.db` | SQLite 文件路径 |
| `AQUA_LOG_LEVEL` | | `info` | `debug` / `info` / `warn` / `error` |
| `AQUA_LOG_FORMAT` | | `text` | `text` / `json`（json 便于采集） |
| `AQUA_RELAY_GROUP` | | `default` | 未指定分组的令牌走哪个分组的渠道与计价 |
| `AQUA_SMTP_*` | | — | 邮件通道（host/port/username/password/from/from_name） |
| `AQUA_EPAY_KEY` | | — | 易支付商户密钥（地址与商户号在后台填） |
| `AQUA_STRIPE_SECRET_KEY` / `_WEBHOOK_SECRET` | | — | Stripe |
| `AQUA_ALIPAY_PRIVATE_KEY` / `_PUBLIC_KEY` | | — | 支付宝（PEM 或裸 base64） |
| `AQUA_WECHATPAY_APIV3_KEY` / `_PRIVATE_KEY` / `_PLATFORM_PUBLIC_KEY` | | — | 微信支付 APIv3 |
| `AQUA_ADMIN_ALLOW_CIDRS` | | 空=不限 | 后台管理接口 CIDR 白名单（逗号分隔，如 `10.0.0.0/8`） |
| `AQUA_CHANNEL_AUTO_DISABLE_MIN_REQUESTS` | | `0`=关闭 | 窗口内请求数达到该值才参与自动停用判定 |
| `AQUA_CHANNEL_AUTO_DISABLE_SUCCESS_RATE` | | `0.95` | 自动停用的成功率阈值 |
| `AQUA_CHANNEL_AUTO_DISABLE_WINDOW_MINUTES` | | `30` | 统计窗口（分钟） |

---

## 十、常见问题

**Q1：启动就退出，报「AQUA_APP_KEY 未设置」？**
主密钥是唯一必填项。`./aqua -gen-key` 生成后，通过 `.env`（Docker）或
`EnvironmentFile`（systemd）注入；写进配置文件是无效的（安全设计）。

**Q2：本地能访问，外网不行？**
检查 `AQUA_SERVER_LISTEN`：容器内必须 `0.0.0.0:8787`；裸机建议 `127.0.0.1:8787`
再由 Nginx/Caddy 反代，同时检查云厂商安全组放行 443/80。

**Q3：流式回复"卡住半天一次性吐出"？**
反代没关缓冲。Nginx 加 `proxy_buffering off;` 并把 `proxy_read_timeout` 提到 300s。

**Q4：忘了管理员密码？**
在服务器上执行 `./aqua -reset-password <用户名>`（随机生成）或
`./aqua -reset-password <用户名> -new-password '新口令'`。改密会吊销该用户全部旧会话。

**Q5：换了一台机器，数据怎么迁？**
两个文件打包带走：`aqua.db`（数据）+ `.env` 里的 `AQUA_APP_KEY`（密钥）。
放回对应位置、起新实例即可，没有其他状态。

**Q6：日志在哪看？**
systemd 部署：`journalctl -u aqua-api -f`；Docker 部署：`docker logs -f aqua-api`。

**Q7：支持哪些数据库？**
当前仅 SQLite（单文件、零运维、足够单机网关使用）。多实例共享数据库的场景请关注后续版本说明。

**Q8：怎么确认我跑的是哪个版本？**
`./aqua -version`（命令行）、`GET /healthz`（接口）或看站点页脚——
显示 `dev` 表示该二进制构建时未注入版本（功能不受影响）。
