# MiniTunnel Agent 配置与启动 Runbook（供 AI 执行，可直接运行）

> 目标读者：在**目标机器（办公室 Mac mini）上运行的 AI 执行体**（如 Claude Code）。
> 目标：把 MiniTunnel agent 装好并常驻，使其连上线上 relay
> `wss://tunnel.example.com/api/tunnel`，从而本机 client 能经 relay 反向连到
> 这台机器的 SSH(22) 和屏幕共享(5900)。
>
> 本文每一步都给出可复制执行的命令与**预期输出**。命令幂等，可重复运行。

---

## 0. 唯一需要人工提供的机密

只有一个机密：**PSK**（三端共享密钥，必须与 relay 一致）。执行前由运维注入环境变量：

```sh
export MINITUNNEL_PSK='<向运维索取，与 relay/ client 完全一致>'
```

其余参数已固定（见下）。证书是**公开**的自签证书（SAN=`minitunnel-relay`），本文已内嵌，无需外部获取。

```sh
export RELAY='wss://tunnel.example.com/api/tunnel'   # 线上 relay（经 L7 网关的 WS 入口）
export AGENT_ID='office-mini'                            # 本 agent 标识（client 用它定位）
export ALLOW='22,5900'                                   # 允许被访问的本地端口：SSH + 屏幕共享
export REPO_DIR="$HOME/minitunnel"                       # 代码目录
```

---

## 1. 前置检查（不满足则先处理）

```sh
# 1a. 必须能解析并连通线上 relay（应输出 ok）
curl -sS --max-time 15 https://tunnel.example.com/api/healthz; echo

# 1b. 必须有 Go（agent 需本地构建）。没有则用 Homebrew 安装：brew install go
go version || echo "缺少 Go：请先 brew install go"

# 1c. 必须已设置 PSK
[ -n "${MINITUNNEL_PSK:-}" ] && echo "PSK 已设置" || echo "错误：未设置 MINITUNNEL_PSK"
```

预期：`ok`、`go version goX.Y...`、`PSK 已设置`。任一不满足，先解决再继续。

---

## 2. 获取代码

```sh
if [ -d "$REPO_DIR/.git" ]; then
  git -C "$REPO_DIR" pull --ff-only
else
  git clone ssh://git@git.example.com/you/minitunnel.git "$REPO_DIR"
fi
cd "$REPO_DIR"
go build ./... && echo "构建通过"
```

预期：最后输出 `构建通过`。

---

## 3. 开启 macOS 被代理的服务

```sh
# 3a. 远程登录（SSH, 22）
sudo systemsetup -setremotelogin on
sudo systemsetup -getremotelogin            # 预期：Remote Login: On

# 3b. 屏幕共享（5900）
sudo launchctl enable system/com.apple.screensharing
sudo launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist 2>/dev/null || true
# 验证 5900 在监听（预期能看到 *:5900 LISTEN）
sleep 1; lsof -nP -iTCP:5900 -sTCP:LISTEN | grep -q 5900 && echo "屏幕共享在监听" || echo "屏幕共享未监听（可能需在系统设置→通用→共享 手动勾选）"
```

> 注：在受 MDM 管控或开启 TCC 限制的机器上，屏幕共享可能必须在「系统设置 → 通用 →
> 共享 → 屏幕共享」里手动开启。SSH 一般可由 `systemsetup` 直接开。

---

## 3.5（可选）SSH 登录横幅——让终端一连上就有提示

MiniTunnel 转发的是 SSH 原始字节流，**不能**往里插字（会破坏协议），所以「已连上」的
提示要由 mini 上的 sshd 出。用 sshd 的 `Banner`（连接时、认证前打印，比 `/etc/motd`
在 macOS 上更可靠）。macOS 的 sshd 由 launchd 按连接现起，改完**无需重启**，下次
`ssh` 即生效。

```sh
BANNER_FILE=/etc/ssh/minitunnel-banner
HOSTLABEL="$(scutil --get LocalHostName 2>/dev/null || hostname)"
printf '\n✅ 已通过 MiniTunnel 连到 %s（office-mini）\n\n' "$HOSTLABEL" | sudo tee "$BANNER_FILE" >/dev/null
# 幂等追加 Banner 指令（已存在则跳过）
grep -qxF "Banner $BANNER_FILE" /etc/ssh/sshd_config || echo "Banner $BANNER_FILE" | sudo tee -a /etc/ssh/sshd_config >/dev/null
echo "已设置 SSH 登录横幅：$BANNER_FILE"
```

验证：从 client 机器重新 `ssh -p 2222 <user>@127.0.0.1`，在输入密码前应先看到那行
`✅ 已通过 MiniTunnel 连到 ...`。

> 撤销：`sudo sed -i '' "\#^Banner $BANNER_FILE#d" /etc/ssh/sshd_config && sudo rm -f "$BANNER_FILE"`

---

## 4. 写入 relay 公开证书

这是 relay 出示、agent 钉住的**公开**自签证书（不是密钥）。必须与 relay 当前证书一致；
若 relay 换过证书，用新的替换此处内容。

```sh
cd "$REPO_DIR"
cat > cert.pem <<'PEM'
-----BEGIN CERTIFICATE-----
MIIBpzCCAU6gAwIBAgIQdp34k3NN8FELZPveeVqJGTAKBggqhkjOPQQDAjAbMRkw
FwYDVQQDExBtaW5pdHVubmVsLXJlbGF5MB4XDTI2MDYzMDExNTUxOFoXDTMxMDYz
MDEyNTUxOFowGzEZMBcGA1UEAxMQbWluaXR1bm5lbC1yZWxheTBZMBMGByqGSM49
AgEGCCqGSM49AwEHA0IABAj3yrx+Y7RJzqGXeXD3maGFPqaZoTl/1YlGzsOTkdfT
qQ/AtKIRYSkLYv0pcotT2bVgthHgQOJ3dbrm6bVbLKujdDByMA4GA1UdDwEB/wQE
AwIChDATBgNVHSUEDDAKBggrBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MB0GA1Ud
DgQWBBQWaDBmD6DmCejMMZ7FpKSi4mfQrDAbBgNVHREEFDASghBtaW5pdHVubmVs
LXJlbGF5MAoGCCqGSM49BAMCA0cAMEQCIDDOzThkf+yXhjQyMOHUkxYlOE554O/B
3bZTzxaGZnHuAiAznsZ9nmgWW2456tBowwTOsRyVmbSDkodBx7DktCMrUA==
-----END CERTIFICATE-----
PEM
# 校验是 minitunnel-relay 证书（预期 subject= CN=minitunnel-relay）
openssl x509 -in cert.pem -noout -subject
```

预期：`subject=CN=minitunnel-relay`。

---

## 5. 安装为常驻服务（LaunchDaemon）并启动

`deploy/install-agent.sh` 会：构建 agent → 安装到 `/usr/local/bin` → 写
LaunchDaemon（开机自启、退出自拉起、PSK 仅 root 可读）→ 设置 `pmset` 防睡眠。

```sh
cd "$REPO_DIR"
RELAY="$RELAY" AGENT_ID="$AGENT_ID" ALLOW="$ALLOW" MINITUNNEL_PSK="$MINITUNNEL_PSK" \
  ./deploy/install-agent.sh
```

预期结尾出现 `Done. The agent is running as a LaunchDaemon ...`。

---

## 6. 验证

```sh
# 6a. 服务在运行（预期能打印 state = running）
sudo launchctl print system/com.minitunnel.agent | grep -E "state =|program =" | head

# 6b. 日志里出现注册成功行（最关键的一行）
sleep 2; tail -n 20 /var/log/minitunnel-agent.log
```

**判定成功的关键日志**：
```
registered with relay as "office-mini" (control link ... -> ...)
```
看到这行即表示 agent 已连上线上 relay。若反复出现
`control link lost: ...; retrying in 3s`，见第 7 节。

可选（需运维提供 dashboard 令牌 `ADMIN_TOKEN`）——确认 relay 端已看到本 agent：
```sh
curl -sS "https://tunnel.example.com/api/api/status?token=$ADMIN_TOKEN" | head -c 400; echo
# 预期 agents 数组里含 {"id":"office-mini",...}
```

最终端到端（在 **client 机器**上，非本机）：
```sh
ssh -p 2222 <你的用户名>@127.0.0.1        # client 已 -L 2222:22 时，应能登上本 mini
```

---

## 7. 故障排查

| 现象 | 处理 |
| --- | --- |
| `curl .../api/healthz` 不是 `ok` | relay 没起或网络不通；先排查网络/relay，agent 连不上是必然。 |
| 日志 `inner TLS handshake: x509: ... unknown authority` | `cert.pem` 与 relay 证书不一致。用 relay 当前的公开证书替换第 4 步内容，重跑第 5 步。 |
| 日志 `websocket dial: ... 426` 或一直重连 | 网关那条 `/api` 路由没开 WebSocket（APISIX `enable_websocket`）。由运维在网关侧打开。 |
| 日志 `bad pre-shared key` / 注册不上 | `MINITUNNEL_PSK` 与 relay 不一致。重设正确 PSK，重跑第 5 步。 |
| client 端报 `agent "office-mini" is not online` | 本 agent 没连上（看本机日志按上面排查），或 `AGENT_ID` 与 client 的 `MINITUNNEL_AGENT` 不一致。 |
| 屏幕共享连不上但 SSH 正常 | 第 3b 未生效，去系统设置手动开启屏幕共享。 |

---

## 8. 运维操作

```sh
# 看实时日志
tail -f /var/log/minitunnel-agent.log
# 停止 / 重启
sudo launchctl bootout system/com.minitunnel.agent
sudo launchctl bootstrap system /Library/LaunchDaemons/com.minitunnel.agent.plist
# 升级（拉新代码后重跑安装脚本即可，幂等）
cd "$REPO_DIR" && git pull --ff-only && \
  RELAY="$RELAY" AGENT_ID="$AGENT_ID" ALLOW="$ALLOW" MINITUNNEL_PSK="$MINITUNNEL_PSK" ./deploy/install-agent.sh
# 彻底卸载
sudo launchctl bootout system/com.minitunnel.agent 2>/dev/null || true
sudo rm -f /Library/LaunchDaemons/com.minitunnel.agent.plist /usr/local/bin/minitunnel-agent
```

---

## 附：一键执行块（确认前置满足后整段运行）

```sh
set -euo pipefail
export MINITUNNEL_PSK='<向运维索取>'                       # ← 唯一需要填的机密
export RELAY='wss://tunnel.example.com/api/tunnel'
export AGENT_ID='office-mini'
export ALLOW='22,5900'
export REPO_DIR="$HOME/minitunnel"

curl -sS --max-time 15 https://tunnel.example.com/api/healthz | grep -qx ok
[ -n "$MINITUNNEL_PSK" ]

if [ -d "$REPO_DIR/.git" ]; then git -C "$REPO_DIR" pull --ff-only
else git clone ssh://git@git.example.com/you/minitunnel.git "$REPO_DIR"; fi
cd "$REPO_DIR"

sudo systemsetup -setremotelogin on
sudo launchctl enable system/com.apple.screensharing || true
sudo launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist 2>/dev/null || true

cat > cert.pem <<'PEM'
-----BEGIN CERTIFICATE-----
MIIBpzCCAU6gAwIBAgIQdp34k3NN8FELZPveeVqJGTAKBggqhkjOPQQDAjAbMRkw
FwYDVQQDExBtaW5pdHVubmVsLXJlbGF5MB4XDTI2MDYzMDExNTUxOFoXDTMxMDYz
MDEyNTUxOFowGzEZMBcGA1UEAxMQbWluaXR1bm5lbC1yZWxheTBZMBMGByqGSM49
AgEGCCqGSM49AwEHA0IABAj3yrx+Y7RJzqGXeXD3maGFPqaZoTl/1YlGzsOTkdfT
qQ/AtKIRYSkLYv0pcotT2bVgthHgQOJ3dbrm6bVbLKujdDByMA4GA1UdDwEB/wQE
AwIChDATBgNVHSUEDDAKBggrBgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MB0GA1Ud
DgQWBBQWaDBmD6DmCejMMZ7FpKSi4mfQrDAbBgNVHREEFDASghBtaW5pdHVubmVs
LXJlbGF5MAoGCCqGSM49BAMCA0cAMEQCIDDOzThkf+yXhjQyMOHUkxYlOE554O/B
3bZTzxaGZnHuAiAznsZ9nmgWW2456tBowwTOsRyVmbSDkodBx7DktCMrUA==
-----END CERTIFICATE-----
PEM

RELAY="$RELAY" AGENT_ID="$AGENT_ID" ALLOW="$ALLOW" MINITUNNEL_PSK="$MINITUNNEL_PSK" \
  ./deploy/install-agent.sh
sleep 2; tail -n 20 /var/log/minitunnel-agent.log
```
