# conch

[English](README.md) | **中文**

一个轻量级、零依赖的 Shell 执行服务器，支持 SSE 流式输出和端到端加密。将任意设备的 Shell 暴露到网络上 — Linux 服务器、Termux（Android）、Windows — 并通过加密连接安全远程控制。附带 MCP 二进制文件，可直接集成到 Claude Desktop 中。

## 特性

- **端到端加密** — X25519 ECDH 密钥交换 + AES-256-GCM，由预共享 API 密钥认证
- **HMAC 请求签名** — 证明 API 密钥持有权，无需传输密钥本身
- **防重放保护** — 基于时间桶的 nonce 追踪，支持 ±5 分钟时钟偏差
- **SSE 流式输出** — stdout 和 stderr 逐行实时推送
- **速率限制** — 每个 IP 令牌桶限流（20 req/s 持续，突发 40）
- **单二进制文件** — 零依赖，静态链接，约 8 MiB
- **跨平台** — Linux（arm64/amd64）、Windows（amd64）、Termux（Android）
- **系统服务** — 支持 systemd（Linux）、runit（Termux）、nssm（Windows）
- **MCP 支持** — 独立的 `conch-mcp` 二进制文件，桥接 Claude Desktop（stdio/JSON-RPC）到远程 Conch 服务器

## 一键安装

**Linux / Termux：**
```bash
curl -fsSL https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.sh | sudo bash
```

**Windows（以管理员身份运行 PowerShell）：**
```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.ps1 | iex
```

脚本会自动完成：下载对应平台的预编译二进制 → 生成 API 密钥 → 注册系统服务。

自定义安装：
```bash
# 指定端口和密钥
sudo ./scripts/install.sh --port 14216 --api-key "your-key"

# Termux（无需 root）
./scripts/install.sh --port 8080

# Windows 自定义端口
.\scripts\install.ps1 -Port 8080 -ApiKey "your-key"
```

## 从源码编译（手动）

```sh
git clone git@github.com:newo-ether/conch.git
cd conch

# 服务端
go build -o conch .
./conch  # 需设置 CONCH_API_KEY=...

# MCP 二进制
go build -o conch-mcp ./cmd/mcp/
```

### 交叉编译

```sh
make all              # 服务端: linux-arm64, linux-amd64, windows-amd64
make mcp-all          # MCP:    linux-arm64, linux-amd64, windows-amd64
```

编译产物输出到 `build/` 目录。

## 快速开始

```sh
export CONCH_API_KEY="your-secret-key"
./conch

# 或通过安装脚本：
sudo ./scripts/install.sh --api-key "your-secret-key"
```

验证：
```sh
curl -s http://localhost:14216/health
# {"status":"ok"}
```

## 配置

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| `CONCH_HOST` | `0.0.0.0` | 监听地址 |
| `CONCH_PORT` | `14216` | HTTP 监听端口 |
| `CONCH_API_KEY` | _（必填）_ | 预共享密钥，用于 HMAC 签名和密钥交换验证。除非设置 `CONCH_ALLOW_NO_AUTH=true`，否则服务端会拒绝启动。 |
| `CONCH_ALLOW_NO_AUTH` | `false` | 允许无密钥启动。加密和签名被禁用，所有请求以明文接受。 |
| `CONCH_TIMEOUT` | `30` | 默认命令超时时间（秒） |
| `CONCH_MAX_TIMEOUT` | `120` | 允许的最大超时时间（秒） |

## 平台差异

| 平台 | Shell | 服务管理器 | 进程控制 |
|------|-------|-----------|----------|
| Linux | `/bin/sh` | systemd | SIGTERM → SIGKILL 升级 |
| Termux | `$PREFIX/bin/sh` | runit | `proot` + signal 回退 |
| Windows | `cmd.exe /C` | nssm | Job objects |

在 Termux 上，命令在 `/data/data/com.termux/files/home` 执行。在 Windows 上，`workdir` 默认为 `%USERPROFILE%`。

## 协议

### `GET /health`

无需认证。返回 `{"status":"ok"}`。

### `GET /public-key`

返回服务端持久化 X25519 公钥，附带 HMAC-SHA256(key, nonce|public_key) 签名以防止 MITM 替换攻击：

```json
{
  "public_key": "<base64url 32B>",
  "nonce": "<base64url 12B>",
  "signature": "<64 hex chars>"
}
```

客户端在信任公钥之前应先验证签名。

### `POST /execute`

执行 Shell 命令。每个请求都携带证明 API 密钥持有权的 HMAC 签名。

**请求头：**

| Header | 必填 | 说明 |
|--------|------|------|
| `X-Signature` | 是 | `HMAC-SHA256(key, timestamp\|method\|path\|bodySHA256\|nonce\|clientPubKey)` → 64 hex |
| `X-Timestamp` | 是 | Unix epoch 秒数 |
| `X-Nonce` | 是 | 随机 12 字节 base64url nonce |
| `X-Encryption` | 是 | 必须为 `v1` |
| `X-Client-Public-Key` | 是 | 临时 X25519 公钥（base64url 32B） |
| `Content-Type` | 是 | `application/octet-stream` |

**请求体：** 使用 AES-256-GCM 加密，密钥由 ECDH(client_ephemeral_priv, server_pub) → HKDF-SHA256 派生。

解密后的 JSON 格式：

```json
{
  "command": "ls -la /tmp",
  "timeout_ms": 10000,
  "workdir": "/tmp"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `command` | string | 是 | 待执行的 Shell 命令 |
| `timeout_ms` | integer | 否 | 单次命令超时时间（毫秒），上限为 `CONCH_MAX_TIMEOUT` |
| `workdir` | string | 否 | 命令的工作目录 |

**响应：** `text/event-stream`，数据体为加密后的 base64url 编码 AES-256-GCM 密文：

```
event: line
data: <base64url 密文>

event: result
data: <base64url 密文>
```

**错误响应：** 所有认证失败（签名不匹配、时钟偏差、重放 nonce、缺少请求头）返回 `401`。解密错误返回 `400`。方法错误返回 `405`。

### 协议概览

```
客户端                           服务端
  |                                 |
  |---- GET /public-key ----------->|  (验证 HMAC 签名)
  |<--- {pubKey, nonce, sig} ------|
  |                                 |
  |  ECDH(eph_priv, server_pub) -> AES 密钥
  |  加密命令 JSON
  |  HMAC 签名所有请求字段
  |                                 |
  |---- POST /execute ------------->|  (验证 HMAC, 检查 nonce, 解密)
  |<--- SSE 流 (加密) -------------|
```

## MCP（Claude Desktop 集成）

`conch-mcp` 二进制文件通过 Model Context Protocol 向 Claude Desktop 暴露 `shell_execute` 工具。

### 多设备配置

```json
{
  "mcpServers": {
    "conch": {
      "command": "/path/to/conch-mcp-windows-amd64.exe",
      "env": {
        "CONCH_DEVICES": "{\"pi\":{\"url\":\"http://192.168.1.100:14216\",\"key\":\"xxx\",\"description\":\"Raspberry Pi 4\"},\"laptop\":{\"url\":\"http://192.168.1.200:14216\",\"key\":\"yyy\",\"description\":\"Windows 笔记本\"}}"
      }
    }
  }
}
```

### 单设备配置（旧版）

```json
{
  "mcpServers": {
    "conch": {
      "command": "/path/to/conch-mcp-windows-amd64.exe",
      "env": {
        "CONCH_SERVER_URL": "http://<host>:14216",
        "CONCH_API_KEY": "<your-api-key>"
      }
    }
  }
}
```

### 工具模式

**`shell_execute`** — 在远程 Conch 服务器上执行命令。

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `device` | string | 是* | — | 目标设备名称（管理多设备时必填） |
| `command` | string | 是 | — | 待执行的 Shell 命令 |
| `timeout_ms` | integer | 否 | 30000 | 超时时间（毫秒，最大 120000） |
| `workdir` | string | 否 | — | 工作目录 |

## 许可证

MIT — 详见 [LICENSE](LICENSE)。
