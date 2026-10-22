# OCS-Tiku fork

本仓库是 [OCS-Tiku](https://github.com/onprs/OCS-Tiku) 的独立 fork，供 ByebyeOCS 正式题库服务使用，同时保留上游本地题库服务。

上游基线为 `aedb64fb0b55b633362558c627887d0f1f4de565`，完整上游提交历史保留在本仓库中。维护和同步方式见 [UPSTREAM.md](UPSTREAM.md)。

## 功能

- 保留上游 `POST /api/search` 兼容接口，旧 OCS 客户端可以继续使用。
- 新增 `POST /api/v2/answers`：请求级公开模型、`cache_first`、`always_llm`、精确缓存、缓存失效、幂等和调用分解。
- 模型调用经过上游共享核心，支持 OpenAI 兼容、Anthropic、Gemini 和 Ollama 协议。
- 正式 platform 服务使用独立 PostgreSQL 数据库与 `tiku` schema；不打开 SQLite，不做双数据库兼容。
- 服务端回传模型调用与 token 事实。缓存命中 token 为 0；用量缺失或总量矛盾标记为 `usage_incomplete`，不自动写入缓存。
- 支持 `single`、`multiple`、`judgement`、`completion` 和 `shortanswer` 题型；简答题保留完整答案文本。
- 模型能力画像、启停、可达性/功能性测试、用量汇总和缓存失效由内部管理接口提供。

## 安装与启动

正式 platform 服务需要 Go 1.26 或更高版本、PostgreSQL 16 和独立的数据库角色。

```sh
go build ./cmd/ocs-tiku-fork
cp ocs-tiku-fork.example.json ocs-tiku-fork.json
./ocs-tiku-fork config check -config ocs-tiku-fork.json
./ocs-tiku-fork migrate -config ocs-tiku-fork.json
./ocs-tiku-fork serve -config ocs-tiku-fork.json
```

`migrate` 必须显式执行，`serve` 不会自动迁移，也不会创建默认模型或密钥。

配置只保存密钥文件路径，不保存密钥内容。数据库连接串、服务令牌和每个模型的 API Key 分别放在权限受控的文件中。`upstream_ca_file` 可选，用于私有 CA；`allow_loopback_upstreams` 只用于本地受控测试。

模型条目示例：

```json
{
  "id": "standard",
  "display_name": "标准模型",
  "protocol": "openai-chat",
  "vendor": "",
  "base_url": "https://models.example.com/v1",
  "model": "example-model",
  "api_key_file": "secrets/standard.key",
  "vision": true,
  "tool_call": false,
  "reasoning": false,
  "context": 128000,
  "disabled": false,
  "max_output_tokens": 2048
}
```

`id` 是主平台保存的公开引用；`base_url`、真实模型名和密钥只存在于 fork 配置。`protocol` 省略时使用 `openai-chat`。

### v1 本地题库页面

上游本地服务仍可单独构建和运行。网页构建需要 Node.js 22 与 npm：

```sh
npm --prefix frontend ci
npm --prefix frontend run build
go run .
```

默认构建读取 `frontend/dist`。需要生成包含网页资源的单文件可执行文件时使用 `ocs_frontend` 构建标签；发布脚本已包含该标签。正式 platform 二进制不需要 Node.js 或网页资源。

## v2 请求

```http
POST /api/v2/answers
Authorization: Bearer <服务令牌>
Idempotency-Key: <请求幂等键>
Content-Type: application/json
```

```json
{
  "request_id": "qq_...",
  "model_offering_id": "standard",
  "mode": "cache_first",
  "cache_scope": "user",
  "cache_scope_key": "<不可逆用户范围键>",
  "question": {
    "title": "简述光合作用的条件。",
    "options": "",
    "type": "shortanswer",
    "images": []
  },
  "budget": { "max_output_tokens": 1024 }
}
```

`mode` 为 `cache_first` 或 `always_llm`：

- `cache_first` 只读精确缓存；未命中时调用模型，完整成功后写入缓存。
- `always_llm` 跳过缓存读取，每次调用模型；完整成功答案仍写入缓存，供以后 `cache_first` 使用。

缓存只做规范化后的完全一致匹配，相似题和历史模糊检索不会作为缓存命中。缓存命中返回 `usage.total_tokens: 0` 和空的 `usage.calls`。

## 测试

```sh
go test ./...
BYEBYEOCS_TIKU_TEST_DATABASE_DSN_FILE=/path/to/test-admin.dsn go test ./...
```

PostgreSQL 集成测试会创建并清理独立临时数据库，要求测试角色具有 `CREATEDB`。未提供连接文件时，相关测试显式跳过。模型夹具使用本地 TLS 服务，不向真实模型发送请求。

## 说明

题库服务只回传 token 与调用事实，不保存钱包金额，不按自己的价格扣费。实际价格、结算和真实支付由主平台配置。

上游仓库目前没有 LICENSE 文件；本 fork 不擅自添加许可证。公开分发或商业使用前需由项目所有者补充许可证并核对依赖许可。
