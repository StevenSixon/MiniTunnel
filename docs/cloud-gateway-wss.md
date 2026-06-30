# 云平台域名网关部署（云上 DevOps 平台 / APISIX，WebSocket 方案）

把 relay 部署到只暴露**域名**、且重启会换 IP 的云平台（如云上 DevOps 平台）时的完整方案。
本文聚焦经由平台 **L7 HTTP(S) 网关** 的 WebSocket 部署；若平台能给 L4 透传，见
末尾「附录：L4 直连」。

- 仓库根 README：四个程序的总体说明与本地用法。
- `internal/proto/ws.go`：WebSocket 传输实现（`DialRelay` / `AcceptWS`）。
- `deploy/`：容器与守护进程的部署文件。

---

## 1. 背景与问题

MiniTunnel 的信任模型是**客户端 pin 固定证书**：client/agent 用 `RootCAs` 钉住
relay 的自签证书，并校验固定 SAN `minitunnel-relay`，**与域名/IP 无关**。所以 IP
变化本身不是问题。

真正的问题在网关层。线上 `tunnel.example.com` 是平台的 **L7 HTTP 网关**
（APISIX，响应头 `server: gateway` / `x-apisix-upstream-status`）：

- 它用平台自己的证书（`*.example.com` DigiCert）**终结 TLS**，client 钉的是
  `minitunnel-relay` → pin 校验失败：
  `x509: certificate is valid for *.example.com, not minitunnel-relay`。
- 它按 **HTTP** 语义路由，而 MiniTunnel 是裸 TLS 字节流，不是 HTTP。

> 结论：裸 TLS 流穿不过 L7 HTTP 网关。要么拿到 L4 透传，要么把流量包成
> **WebSocket** 走 L7。云上 DevOps 平台当前只提供 L7，故采用 WebSocket。

---

## 2. 方案对比

| 平台能提供的入口 | 方案 | 代码/配置 | 备注 |
| --- | --- | --- | --- |
| 独立 L4 TCP 端口 | 裸 socket 直连 | `MINITUNNEL_RELAY=域名:端口` | 最干净，零改动；需平台开 stream route |
| 复用 443 的 L4 SNI 透传 | 裸 socket + SNI | 另加 `MINITUNNEL_SNI=域名` | 托管网关通常不开放 |
| **仅 L7 HTTP(S) 网关** | **WebSocket** | `MINITUNNEL_RELAY=wss://域名/前缀/tunnel` | **本文方案**；需路由开 WebSocket |

---

## 3. WebSocket 方案原理

把 relay 现有的 HTTP 服务（healthz / dashboard 所在的 `:8080`）再挂一个
WebSocket 端点 `<前缀>/tunnel`。client/agent 连 `wss://域名/前缀/tunnel`，并把
**原有的 pinned TLS 跑在 WebSocket 内层**：

```
client ─ wss://(网关 DigiCert，系统根 CA 验证) ─▶ 云上 DevOps 平台 L7 网关 ─ /api/tunnel ─▶ relay:8080
   └──────────── 你的 minitunnel-relay pinned TLS，端到端 ────────────┘
                 网关只看到不透明的加密帧，看不到 PSK 和明文流量
```

- 外层 `wss` 用平台证书（标准 HTTPS，系统根 CA，无需 pin）——只为穿过网关。
- 内层仍是你的自签 pinning + PSK——**端到端加密不丢，平台看不到内容**。
- relay 的裸 TLS 监听（`MINITUNNEL_ADDR`，默认 `:7000`）照常存在；两个入口都汇入
  同一个 `relay.handle`。

唯一第三方依赖：`github.com/coder/websocket`（CLAUDE.md 记录的 stdlib-only 例外）。

---

## 4. 部署步骤

### 4.1 生成证书（一次性，本地）

```sh
go run ./cmd/gencert            # 生成 cert.pem + key.pem（SAN 固定为 minitunnel-relay）
base64 < cert.pem | tr -d '\n'  # → MINITUNNEL_CERT
base64 < key.pem  | tr -d '\n'  # → MINITUNNEL_KEY
```

同一份 `cert.pem` 由 relay 出示、agent/client 钉住；`key.pem` 只在 relay。

### 4.2 relay（云上 DevOps 平台容器）

构建镜像（仓库**根目录**执行）：

```sh
docker build -f deploy/Dockerfile -t minitunnel-relay:latest .
```

容器环境变量（模板见 `deploy/relay.env.example`）：

```
MINITUNNEL_PSK=<长随机串>            # openssl rand -hex 24
MINITUNNEL_HTTP_ADDR=:8080          # 网关路由到的 HTTP 监听（WS 端点所在）
MINITUNNEL_HTTP_PREFIX=/api         # 与平台路由 /api/* 对齐
MINITUNNEL_ADMIN_TOKEN=<dashboard 令牌>
MINITUNNEL_ADDR=:7000               # 裸 TLS 监听，经网关时用不到，保留即可
MINITUNNEL_CERT=<cert 的 base64>
MINITUNNEL_KEY=<key 的 base64>
```

云上 DevOps 平台平台侧：

1. 服务/路由指向**容器端口 8080**（即现在 `/api/*` 那条 HTTP 路由）。
2. **该路由开启 WebSocket upgrade**（APISIX：`enable_websocket: true`）——本方案唯一前提。
3. 健康检查路径设为 `<前缀>/healthz`（如 `/api/healthz`）。

### 4.3 agent

**办公室 Mac mini（推荐：LaunchDaemon，不要用容器）**

```sh
RELAY='wss://tunnel.example.com/api/tunnel' AGENT_ID=office-mini \
  MINITUNNEL_PSK=... ./deploy/install-agent.sh
```

先在「系统设置 → 通用 → 共享」开启**远程登录**(SSH:22) 和**屏幕共享**(5900)。

> 为什么不容器化：agent 始终连 `127.0.0.1:<端口>`，容器里的 loopback 不是 mac
> 宿主的 loopback，Docker Desktop 的 host 网络也不暴露 mac 宿主 loopback，故容器内
> agent 够不到 mini 的 sshd / 屏幕共享。

**Linux 主机（容器）**

```sh
docker build -f deploy/Dockerfile.agent -t minitunnel-agent:latest .
docker run -d --name minitunnel-agent --network host \
  --env-file agent.env minitunnel-agent:latest
```

- 必须 `--network host`（Linux），否则 agent 只能够到同容器/同 pod 的 loopback。
- agent 要**对外**拨 wss，镜像含 `ca-certificates` 以校验网关证书；内层 pin 用
  `MINITUNNEL_CERT`，与系统根无关。
- 环境变量模板见 `deploy/agent.env.example`。

### 4.4 client（你的机器）

```sh
export MINITUNNEL_PSK=<同一个 PSK>
export MINITUNNEL_CERT=<同一张 cert.pem，或其 base64>
./client -relay wss://tunnel.example.com/api/tunnel -agent office-mini \
  -L 2222:22 -L 5901:5900
```

然后：`ssh -p 2222 you@127.0.0.1`、`open vnc://127.0.0.1:5901`。

---

## 5. 环境变量参考

| 变量 | relay | agent | client | 说明 |
| --- | :---: | :---: | :---: | --- |
| `MINITUNNEL_PSK` | ✓ | ✓ | ✓ | 共享密钥，三端一致 |
| `MINITUNNEL_CERT` | ✓ | ✓ | ✓ | relay 自签证书；relay 出示、两端 pin（路径/内联 PEM/单行 base64）|
| `MINITUNNEL_KEY` | ✓ | | | relay 私钥，仅 relay |
| `MINITUNNEL_RELAY` | | ✓ | ✓ | `host:port` 或 `ws(s)://域名/前缀/tunnel` |
| `MINITUNNEL_HTTP_ADDR` | ✓ | | | HTTP 监听；WS 端点 + healthz + dashboard |
| `MINITUNNEL_HTTP_PREFIX` | ✓ | | | URL 前缀，与网关路由对齐（如 `/api`）|
| `MINITUNNEL_ADMIN_TOKEN` | ✓ | | | 保护 dashboard / `/api/status`（不保护 tunnel）|
| `MINITUNNEL_ADDR` | ✓ | | | 裸 TLS 监听，默认 `:7000` |
| `MINITUNNEL_ID` | | ✓ | | agent 标识 |
| `MINITUNNEL_AGENT` | | | ✓ | 目标 agent 标识 |
| `MINITUNNEL_ALLOW` | | ✓ | | 允许被访问的本地端口，默认 `22,5900` |
| `MINITUNNEL_FORWARD` | | | ✓ | `本地端口:远端端口`，逗号分隔 |
| `MINITUNNEL_SNI` | | ✓ | ✓ | 仅 L4 SNI 透传时用；wss 方案留空 |

优先级：命令行 flag > 环境变量 > 内置默认。

---

## 6. 验证清单

1. `curl https://tunnel.example.com/api/healthz` → `ok`（relay 在线）。
2. agent 日志出现 `registered with relay as "office-mini"`。
3. dashboard（`https://tunnel.example.com/api/?token=...`）能看到 agent 在线。
4. `ssh -p 2222 you@127.0.0.1` 能登上 mini → 全链路通。

---

## 7. 故障排查

| 现象 | 原因 / 处理 |
| --- | --- |
| `502 Bad Gateway` / `x-apisix-upstream-status: 502` | 网关活着但上游不通：relay 没起，或路由 upstream 没指到容器 `:8080`。 |
| client 日志 `x509: certificate is valid for *.example.com, not minitunnel-relay` | 走到了 L7 的 443 终结点（裸 TLS）。改用 `wss://.../api/tunnel`，别直连 443。 |
| WS 握手失败 / 立刻断开 | 路由没开 `enable_websocket`。在 APISIX 路由上打开。 |
| 内层 `inner TLS handshake: x509: certificate signed by unknown authority` | agent/client 的 `MINITUNNEL_CERT` 和 relay 的证书不是同一张。用同一份 cert.pem。 |
| 容器内 agent 连不上本地服务 | agent 只够到自身 loopback。Linux 用 `--network host`；mac mini 改用 LaunchDaemon。 |
| control link 周期性重连 | 网关 idle 超时掐长连接。agent 每 30s ping、断后每 3s 重连兜底；可调大网关超时。 |

---

## 8. 安全说明

- 端到端加密：内层 pinned TLS 在 WebSocket 内运行，**网关只见密文**，学不到 PSK
  或流量明文。
- 信任锚定在固定 SAN `minitunnel-relay`，与域名/IP 解耦；外层 wss 仅借平台证书穿网关。
- `<前缀>/tunnel` 不挂 admin token：它由 PSK + 内层 pinning 自证；扫描者升级 WS 后会
  在内层握手/PSK 校验被拒。
- 证书与 PSK 经环境变量注入，不写进镜像（见 `.dockerignore`）。

---

## 附录：L4 直连（若平台支持）

若云上 DevOps 平台能给**独立 L4 TCP 端口**或**SNI stream route**，则无需 WebSocket，现有 pinned
TLS 直接可用：

- 独立端口：`MINITUNNEL_RELAY=tunnel.example.com:<port>`，`MINITUNNEL_SNI` 留空。
- 共享 443 的 SNI 透传：另加 `MINITUNNEL_SNI=tunnel.example.com`（SNI 仅用于路由，
  证书仍 pin 到 `minitunnel-relay`）。

向平台申请的原话：「给我一个独立四层 TCP 端口（APISIX stream route），原样转发到容器
`:7000`，不要终结 TLS、不要按 HTTP 解析。」
