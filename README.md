# M365Copilot2ApiX

<p align="center">
  <strong>Microsoft 365 Copilot → OpenAI / Anthropic 兼容 API 网关(带代理节点池)</strong>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" />
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=111827" />
  <img alt="API" src="https://img.shields.io/badge/API-OpenAI%20Compatible-412991?logo=openai" />
  <img alt="API" src="https://img.shields.io/badge/API-Anthropic%20Compatible-FF6B6B?logo=anthropic" />
</p>

M365Copilot2ApiX 把微软 365 Copilot 商业订阅背后的 **ChatHub 私有协议**(WebSocket)翻译成标准的 **OpenAI / Anthropic 兼容 API**。Claude Code、OpenCode、Cursor 以及任何 OpenAI/Anthropic 客户端都可以直接调用 M365 Copilot。

**内置代理节点池**:支持代理节点抓取(14 个免费源)、多协议(ss/ssr/vmess/vless/trojan/hysteria2/http/socks5)、自动测活(只测微软可达 + 持续 10MB 稳定性)、动态打分、负载均衡。

> ⚠️ **免责声明**:本项目不是微软官方产品,与 Microsoft、OpenAI、Anthropic 无任何从属关系。仅供个人学习与研究。

## 功能特性

| 功能 | 说明 |
|------|------|
| OpenAI 兼容 `/v1/chat/completions` | 支持流式输出与 function calling |
| OpenAI Responses `/v1/responses` | 兼容 Responses 协议(Codex 等客户端) |
| Anthropic 兼容 `/v1/messages` | Claude Code / Cursor 直连 |
| SSE 流式输出 | 逐字实时返回,`stream: true` |
| 工具调用转换 | OpenAI function calling ⇄ M365 工具协议 |
| 多模态输入 | 支持图片附件(base64 data URL / https URL) |
| 多账号管理 | PKCE 浏览器授权 + Device Code + Refresh Token 导入 + ROPC |
| 账号轮询与故障转移 | 多账号轮询均衡,故障自动切换 |
| **代理节点池** | 多协议代理抓取/测活/负载均衡 |
| **代理抓取** | 14 个免费源,支持 base64/clash.yml/singbox.json/纯文本 |
| **三层测活** | TCP 连通 → 微软可达 → 持续 10MB 下载稳定性 |
| **动态打分** | 报错扣分,持续可用加分,自动禁用不可靠节点 |
| **负载均衡** | 按分数加权分配请求,不可靠节点倾斜到可靠节点 |
| API Key 管理 | 控制台创建/撤销/回读 |
| SQLite/PostgreSQL | 单实例 SQLite,多实例 PostgreSQL |
| Redis 运行态 | 单实例内存,多实例 Redis |

## 快速开始

### Docker(推荐)

```bash
git clone https://github.com/xiaocongyu66/m365-copilot2xapi.git
cd m365-copilot2xapi
cp config.example.yaml config.yaml
```

生成密钥并编辑 `config.yaml`:

```bash
openssl rand -hex 32    # jwtSecret
openssl rand -base64 32 # credentialEncryptionKey
```

```yaml
secrets:
  jwtSecret: "<生成的 hex>"
  credentialEncryptionKey: "<生成的 base64>"

bootstrapAdmin:
  username: "admin"
  password: "<强密码>"
```

启动:

```bash
docker compose pull
docker compose up -d
docker compose logs -f
```

打开 `http://127.0.0.1:8000`,用管理员账号登录。

### 源码编译

```bash
cp config.example.yaml config.yaml
make run
```

### 初始化与第一次调用

1. 用管理员密码登录控制台(首次登录强制改密码)
2. 在「账号」页点击**开始授权**:
   - **PKCE 浏览器授权**:弹窗登录 Microsoft,复制回调 URL 粘贴
   - **Device Code**:终端显示设备码,另一台设备输入
   - **Refresh Token 导入**:直接粘贴 refresh_token
   - **ROPC 密码授权**:用户名+密码(需 AAD 管理员授权)
3. 授权成功后创建第一个 API Key
4. 用 API 示例验证调用

## 代理节点池配置

在 `config.yaml` 里启用代理池:

```yaml
proxyPool:
  enabled: true              # 启用后请求走节点 IP,不走原始 IP
  fetchEnabled: true         # 启用自动抓取
  fetchInterval: 30m         # 抓取间隔
  fetchSources: []           # 自定义源(为空用内置 14 个免费源)
  checkInterval: 1m           # 每分钟测活一次
  checkConcurrency: 50        # 同时测活 50 个
  scoreDisableThreshold: 0    # 分数低于 0 自动禁用
```

### 支持的代理协议

ss / ssr / vmess / vless / trojan / hysteria2 / http / socks5

### 订阅格式自动检测

抓取器自动检测格式,无需手动指定:
- **base64 订阅**(v2ray 格式,每行一个链接)
- **clash.yml**(Clash 配置文件,proxies 字段)
- **singbox.json**(sing-box 配置,outbounds 字段)
- **纯文本**(每行一个链接,或 ip:port 格式)

### URL scheme 标注

对于纯 ip:port 格式的代理列表,可用 `#scheme=` 标注给每行加协议前缀:

```
https://example.com/http.txt#scheme=http
https://example.com/socks5.txt#scheme=socks5
```

### 打分机制

- **报错扣分**:每次请求报错 -2 分
- **测活失败**:-20 分
- **持续可用**:每分钟 +1 分
- **测活通过**:+5 分
- 分数低于 0 自动禁用,恢复可用自动启用

### 负载均衡

启用节点后,所有请求走节点 IP(不走原始 IP):
- 多节点平均分配(如 12 请求 / 2 节点 = 各 6 个)
- 不可靠节点少分配,可靠节点多分配(按分数加权)
- 没有可用节点时回退直连

### 三层测活

1. **TCP 连通性**:快速排除死节点(3 秒超时)
2. **微软可达性**:测试 `login.microsoftonline.com` 是否可达
3. **持续 10MB 下载**:从微软 CDN 下载 10MB,10 秒内不断流才算稳定

## API 示例

### Chat Completions

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

### Anthropic Messages

```bash
curl http://127.0.0.1:8000/v1/messages \
  -H "x-api-key: YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

## 配置说明

全部通过 `config.yaml` 配置。详见 `config.example.yaml`。

## 架构

```
┌──────────────┐  OpenAI/Anthropic  ┌──────────────────┐  ChatHub  ┌──────────────┐
│ Claude Code  │ ──────────────────►│     网关          │ ─────────►│ M365 Copilot │
│ OpenCode     │  /v1/chat/...       │ (Go)              │  WebSocket│   (云端)     │
│ 任意客户端    │  /v1/messages      │                   │          │              │
└──────────────┘                    └──────────────────┘          └──────────────┘
                                           │ 走节点 IP
                                    ┌──────┴──────┐
                                    │  代理节点池   │
                                    │ 抓取+测活+LB │
                                    └─────────────┘
```

## License

MIT
