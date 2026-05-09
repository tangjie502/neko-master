# VPS Agent Data Contract

本文档记录当前 Neko Master agent 上报协议的事实，以及 VPS agent 第一版应如何映射数据。它不是新协议设计稿；后续 `vps-agent` MVP 应优先兼容这里描述的现有 `POST /api/agent/heartbeat` 和 `POST /api/agent/report`。

## 结论

Neko Master 现有 agent 协议可以复用，但只能接收已经计算好的流量增量。它不会从 xray、hysteria、vnstat、ss 或 journal 里主动采集数据。

VPS agent 必须在 VPS 本机完成这些工作：

- 识别 `backendId` 和 agent token。
- 采集并归一化流量增量。
- 将使用者 IP、目标域名、目标 IP、节点端口、协议/应用分类映射为现有 `TrafficUpdate` 字段。
- 周期性调用 `POST /api/agent/report` 上报。
- 周期性调用 `POST /api/agent/heartbeat` 维持在线状态。

## 认证和后端要求

两个 agent 入口都使用同一套校验：

- `backendId` 必须是正整数。
- 后端必须存在。
- 后端 `url` 必须是 agent mode，即以 `agent://` 开头。
- 后端 `token` 必须非空。
- 请求 token 必须等于后端 token。
- token 可放在 `Authorization: Bearer <token>` 或 `x-agent-token: <token>`。
- `agentId` 必须非空，截断到 128 字符。
- 同一个 `backendId` 默认只允许绑定一个在线 `agentId`。已有 agent 离线超过 10 秒后允许新 `agentId` 重新绑定。
- `protocolVersion` 默认按 `1` 处理，可通过服务端环境变量 `MIN_AGENT_PROTOCOL_VERSION` 提高最低要求。
- 如果服务端配置了 `MIN_AGENT_VERSION`，上报必须带可解析的 `agentVersion` 或 heartbeat 的 `version`。

注意：当前 `backend_configs.type` 已支持 `clash | surge | vps`。VPS 后端必须使用 agent mode，即 `url=agent://...`，并通过 token 认证。

## Heartbeat

Endpoint:

```http
POST /api/agent/heartbeat
Authorization: Bearer <backend token>
Content-Type: application/json
```

Payload:

```json
{
  "backendId": 1,
  "agentId": "vps-45.62.113.232",
  "protocolVersion": 1,
  "agentVersion": "0.1.0",
  "hostname": "vps-hostname",
  "gatewayType": "vps",
  "gatewayUrl": "vps://45.62.113.232",
  "gatewayLatencyMs": 0,
  "serverLatencyMs": 0
}
```

字段说明：

| 字段 | 必填 | 当前处理 |
| --- | --- | --- |
| `backendId` | 是 | 正整数，找对应后端配置 |
| `agentId` | 是 | 非空字符串，最多 128 字符；用于绑定同一 backend |
| `protocolVersion` | 否 | 缺省为 `1` |
| `agentVersion` / `version` | 否 | 只有服务端设置 `MIN_AGENT_VERSION` 时才强制 |
| `hostname` | 否 | 最多 128 字符，写入 `agent_heartbeats.hostname` |
| `gatewayType` | 否 | 最多 16 字符，VPS agent 建议填 `vps` |
| `gatewayUrl` | 否 | 最多 512 字符，VPS agent 可填 `vps://<public-ip>` |
| `gatewayLatencyMs` | 否 | 非负数，四舍五入为整数 |
| `serverLatencyMs` | 否 | 非负数，四舍五入为整数 |

成功响应：

```json
{
  "success": true,
  "backendId": 1,
  "agentId": "vps-45.62.113.232",
  "serverTime": "2026-05-09T10:00:00.000Z"
}
```

Heartbeat 只写入或更新 `agent_heartbeats`，不写任何流量统计表。

## Report

Endpoint:

```http
POST /api/agent/report
Authorization: Bearer <backend token>
Content-Type: application/json
Content-Encoding: gzip
```

`Content-Encoding: gzip` 可选。服务端支持 gzip body，并会移除压缩前的 `content-length` 以避免 Fastify body limit 判断错误。

Payload:

```json
{
  "backendId": 1,
  "agentId": "vps-45.62.113.232",
  "protocolVersion": 1,
  "agentVersion": "0.1.0",
  "requestId": "vps-45.62.113.232-20260509T100000Z-000001",
  "updates": [
    {
      "domain": "youtube.com",
      "ip": "142.250.190.14",
      "chain": "Hysteria-3482",
      "chains": ["Hysteria-3482"],
      "rule": "YouTube",
      "rulePayload": "youtube.com",
      "upload": 123456,
      "download": 987654,
      "connections": 3,
      "sourceIP": "171.88.21.177",
      "timestampMs": 1778320800000
    }
  ]
}
```

顶层字段：

| 字段 | 必填 | 当前处理 |
| --- | --- | --- |
| `backendId` | 是 | 同 heartbeat |
| `agentId` | 是 | 同 heartbeat |
| `protocolVersion` | 否 | 缺省为 `1` |
| `agentVersion` | 否 | 只有服务端设置 `MIN_AGENT_VERSION` 时才强制 |
| `requestId` | 否但强烈建议 | 最多 64 字符；5 分钟内重复会被当作已处理，避免重试重复计费 |
| `updates` | 是 | 数组；空数组会成功返回但不写统计 |

`updates` 单条字段：

| 字段 | 必填 | 当前清洗规则 |
| --- | --- | --- |
| `domain` | 否 | 字符串，最多 253 字符；空字符串表示未知域名 |
| `ip` | 否 | 字符串，最多 64 字符；建议目标 IP 未知时传空字符串 |
| `chain` | 否 | 当 `chains` 为空时作为兜底；默认 `DIRECT` |
| `chains` | 否 | 字符串数组，最多 12 个；统计里会用 `chains.join(" > ")` 作为完整节点链路 |
| `rule` | 否 | 字符串，最多 256 字符；默认 `Match` |
| `rulePayload` | 否 | 字符串，最多 512 字符；有值时部分统计显示为 `rule(rulePayload)` |
| `upload` | 是 | 非负整数；非数字按 0；向下取整 |
| `download` | 是 | 非负整数；非数字按 0；向下取整 |
| `connections` | 否 | 非负整数；不传时服务端用兼容逻辑估算连接数 |
| `sourceIP` | 否 | 字符串，最多 64 字符；用于 Device 维度 |
| `timestampMs` | 否 | Unix epoch milliseconds；缺省为服务端当前时间 |

重要限制：

- `upload === 0 && download === 0` 的 update 会被丢弃。
- `updates` 每批最大条数由 `AGENT_INGEST_MAX_BATCH_SIZE` 控制，默认 5000；超出部分被截断且不会处理。
- `report` 成功响应只代表进入内存缓冲和实时统计，不代表已经同步刷入 SQLite/ClickHouse。
- agent mode 默认约 30 秒 flush 一次，由 `AGENT_FLUSH_INTERVAL_MS` 控制，最低 5 秒。

成功响应：

```json
{
  "success": true,
  "backendId": 1,
  "accepted": 1,
  "dropped": 0
}
```

重复 `requestId` 响应：

```json
{
  "success": true,
  "backendId": 1,
  "accepted": 0,
  "dropped": 0,
  "duplicate": true
}
```

## VPS 字段映射

第一版 VPS agent 应按下表生成 `updates`：

| VPS 语义 | Report 字段 | 建议值 |
| --- | --- | --- |
| 节点使用者 IP | `sourceIP` | 从 Hysteria journal `addr`、Xray access log source、或 `ss` remote address 提取 |
| 出口目标域名 | `domain` | 从 Hysteria `reqAddr` 或 Xray access log target 提取；没有域名则传空字符串 |
| 出口目标 IP | `ip` | 目标 IP；只有域名且无法解析时可传空字符串 |
| 节点端口/协议 | `chain` / `chains[0]` | `Hysteria-3482`、`Xray-25629`、`Xray-59962` |
| 应用分类 | `rule` | `YouTube`、`Google`、`Apple`、`Telegram`、`GitHub`、`Microsoft`、`Hysteria`、`Xray`、`VPS` 等 |
| 分类命中值 | `rulePayload` | 命中的域名后缀，如 `googlevideo.com`；未分类可空 |
| VPS 上传字节 | `upload` | 从 VPS 视角发往外部或客户端的增量字节，必须先统一方向 |
| VPS 下载字节 | `download` | 从 VPS 视角收到的增量字节，必须先统一方向 |
| 请求数/连接数 | `connections` | 日志事件数、连接数或聚合后的请求数 |
| 事件时间 | `timestampMs` | 采集事件实际时间；没有可靠时间时用采集时间 |

建议统一方向：

- `upload`: VPS 对外发送的字节。对代理节点而言通常包含发给用户客户端的下行流量，也可能包含发给目标站点的请求上行，取决于数据源。
- `download`: VPS 从外部收到的字节。对代理节点而言通常包含用户客户端上传到 VPS、目标站点返回到 VPS 的流量。

如果数据源无法按连接/域名拆分字节，只能把总流量写入一个聚合 update，不要伪造精确域名字节。

## 数据源精度约束

| 数据源 | 可用于字节统计 | 可用于 sourceIP | 可用于 domain | 说明 |
| --- | --- | --- | --- | --- |
| `vnstat --json` | 是，整机/interface 增量 | 否 | 否 | 适合总流量趋势；不能归因到用户 IP、域名或端口 |
| `ss -Hnt state established` | 否 | 是，TCP 当前连接 | 否 | 适合 Xray TCP 入口使用者和端口在线观察；没有历史域名和字节归因 |
| `journalctl -u hysteria-server` | 取决于日志内容 | 是 | 是 | 已知 journal 有 `addr` 和 `reqAddr`；若无字节字段，只能统计请求数 |
| Xray access log | 通常否 | 是 | 是 | 当前关闭；开启后若无字节字段，也只能统计请求数 |

当前 `report` 会丢弃零字节 update。因此只统计请求次数的数据源不能直接用 `upload=0, download=0` 上报。第一版可选策略：

1. 对有字节来源的总量，用 `vnstat` 差值生成聚合流量 update。
2. 对只有请求数的日志来源，暂不写 `report`，等后端支持 request-only event。
3. 或者用最小占位字节使其进入统计，但这会污染总流量，不建议作为默认行为。

## 写入路径

`POST /api/agent/report` 的写入路径是：

1. 清洗 `updates` 为 `TrafficUpdate`。
2. 写入 `RealtimeStore`，前端可先看到实时增量。
3. 写入 per-backend `BatchBuffer`。
4. 定时 flush，调用 `db.batchUpdateTrafficStats(...)`。
5. 如果启用 ClickHouse，同时异步写入 ClickHouse。
6. 如果目标 IP 可做 GeoIP，后台查询后进入国家维度缓冲，下一次 flush 写入 country stats。

## 统计表映射

以下表由 `batchUpdateTrafficStats` 根据同一批 `TrafficUpdate` 派生写入。

| 表 | 主维度 | 来自字段 | VPS 用途 |
| --- | --- | --- | --- |
| `domain_stats` | `backend_id + domain` | `domain`, `ip`, `upload`, `download`, `connections`, `rule`, `chains` | 出口域名 Top、域名总流量 |
| `ip_stats` | `backend_id + ip` | `ip`, `domain`, `upload`, `download`, `connections`, `rule`, `chains` | 出口目标 IP Top |
| `device_stats` | `backend_id + source_ip` | `sourceIP`, `upload`, `download`, `connections` | 节点使用者 IP Top |
| `device_domain_stats` | `backend_id + source_ip + domain` | `sourceIP`, `domain`, traffic | 使用者 IP 访问域名明细 |
| `device_ip_stats` | `backend_id + source_ip + ip` | `sourceIP`, `ip`, traffic | 使用者 IP 访问目标 IP 明细 |
| `proxy_stats` | `backend_id + chain` | `chains.join(" > ")`, traffic | 节点端口/协议统计 |
| `rule_stats` | `backend_id + rule` | `rule` 或 `rule(rulePayload)`, `chains[0]`, traffic | 协议或应用分类统计 |
| `rule_chain_traffic` | `backend_id + rule + chain` | `rule`, full chain, traffic | 应用分类按节点端口拆分 |
| `rule_domain_traffic` | `backend_id + rule + domain` | `rule`, `domain`, traffic | 应用分类下的域名 Top |
| `rule_ip_traffic` | `backend_id + rule + ip` | `rule`, `ip`, traffic | 应用分类下的目标 IP Top |
| `domain_proxy_stats` | `backend_id + domain + chain` | `domain`, full chain, traffic | 域名按节点端口拆分 |
| `ip_proxy_stats` | `backend_id + ip + chain` | `ip`, full chain, traffic | 目标 IP 按节点端口拆分 |
| `minute_stats` | `backend_id + minute` | `timestampMs`, traffic, `connections` | 今日/分钟趋势 |
| `hourly_stats` | `backend_id + hour` | `timestampMs`, traffic, `connections` | 小时趋势 |
| `minute_dim_stats` | `backend_id + minute + domain + ip + source_ip + chain + rule` | 所有核心维度 | 时间范围内的精确多维查询 |
| `hourly_dim_stats` | `backend_id + hour + domain + ip + source_ip + chain + rule` | 所有核心维度 | 长时间范围查询 |

`domain_stats` 在 `domain` 为空时不会写入。`ip_stats` 会写入，即使 `ip` 为空；VPS agent 应尽量提供目标 IP，避免空 IP 聚合成无意义记录。

## rule 和 chain 的显示语义

当前写库逻辑会生成两个不同概念：

- `fullChain = chains.join(" > ") || chain || "DIRECT"`
- `ruleName = chains.length > 1 ? chains[chains.length - 1] : rulePayload ? rule + "(" + rulePayload + ")" : rule`

因此 VPS agent 第一版建议：

- 简单端口场景使用单元素 `chains`，例如 `["Hysteria-3482"]`，这样 `ruleName` 会来自 `rule` / `rulePayload`，`chain` 会显示节点端口。
- 不要把应用分类塞进多元素 `chains` 的最后一位，否则 `rule_stats` 会被改写为最后一个 chain。
- 应用分类放 `rule`，命中的规则或域名后缀放 `rulePayload`。

示例：

```json
{
  "domain": "googlevideo.com",
  "ip": "142.250.190.14",
  "chains": ["Hysteria-3482"],
  "rule": "YouTube",
  "rulePayload": "googlevideo.com",
  "upload": 1024,
  "download": 1048576,
  "connections": 1,
  "sourceIP": "171.88.21.177",
  "timestampMs": 1778320800000
}
```

这会产生：

- 节点端口：`Hysteria-3482`
- 应用分类：`YouTube(googlevideo.com)`
- 使用者 IP：`171.88.21.177`
- 出口域名：`googlevideo.com`

如果希望 `rule_stats` 只显示 `YouTube`，则不要传 `rulePayload`，或后续调整后端 `ruleName` 生成逻辑。

## 错误码和失败处理

常见失败：

| HTTP | 条件 |
| --- | --- |
| `400` | `backendId` 无效、非 agent mode、`agentId` 无效 |
| `401` | token 缺失或不匹配 |
| `403` | 后端未配置 token |
| `404` | 后端不存在 |
| `409` | 同一 backend 已被另一个在线 agentId 绑定 |
| `426` | agent protocol 或 agent version 太旧 |

VPS agent 失败策略：

- 网络失败或 5xx：保留本地 buffer，下次重试。
- 401/403/404/426：配置或版本错误，不应无限重试污染日志。
- 409：短暂等待后重试；如果长期冲突，说明同一 backend 有重复 agent。
- 发送 `requestId`，保证重试不会在 5 分钟内重复计费。

## 第一版建议 payload 策略

第一版实现状态：

- `neko-agent --gateway-type vps` 已接入现有 agent 协议。
- `vnstat --json` 会生成 `VPS-total` 聚合流量，用于今日/小时/时间范围趋势。
- `ss -Hnt state established` 会识别 TCP 端口使用者，默认端口为 `25629,59962`。
- `journalctl -u hysteria-server` 会解析 `addr` 和 `reqAddr`，并按域名做基础应用分类。
- 由于当前 report 协议仍要求非零字节，`ss` 和 Hysteria journal 的请求/连接事件第一版使用 `upload=1` 的最小占位字节进入现有统计表。这会让 Top IP / Top 域名 / 应用分类可见，但这些维度的字节数不是精确流量归因。

理想策略仍然是不要把请求数伪装成真实流量。后续应拆成两类 update：

1. 总流量趋势 update：来自 `vnstat` 差值。
   - `domain`: 空字符串
   - `ip`: 空字符串或 VPS 公网 IP
   - `chains`: `["VPS-total"]`
   - `rule`: `VPS`
   - `upload` / `download`: `vnstat` interval delta
   - `connections`: 可填 0 或本周期连接数估计

2. 可归因流量 update：只有当 Hysteria/Xray 日志或其他来源提供可归因字节时才上报域名/IP/使用者维度。
   - 有真实字节：按域名、目标 IP、sourceIP、chain、rule 聚合后上报。
   - 只有请求数：暂不进入 `report`，除非后端新增 request-only ingestion。

这个限制会导致第一版可以准确显示 VPS 总流量趋势，但 Hysteria 目标域名 Top 如果只有 journal 请求日志而没有字节，只能在后续新增请求事件模型后准确展示请求次数；不能用当前 `report` 协议无损表达。
