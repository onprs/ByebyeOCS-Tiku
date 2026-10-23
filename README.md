# ByebyeOCS-Tiku

为 [ByebyeOCS](https://github.com/onprs/ByebyeOCS) 提供模型作答、精确答案缓存和 token 用量查询，兼容 OCS 客户端的 `/api/search` 接口。

## 功能

- 支持单选、多选、判断、填空和简答题，以及图片题。
- 按请求选择已配置模型，支持 OpenAI 兼容、Anthropic、Gemini 和 Ollama 协议。
- `cache_first` 优先查精确缓存，未命中再调用模型；`always_llm` 每次调用模型，成功答案可供后续缓存查询使用。
- 返回输入、输出及总 token、缓存命中和调用明细；缓存命中 token 为零，用量不完整时明确标记。
- 支持模型启停、连通性检查、功能测试、用量汇总和错误缓存失效。
- 相同幂等请求返回保留期内的原结果，过期请求保留记录以防重复调用；价格与钱包结算由 ByebyeOCS 管理。

## 安装与配置

从源码构建需要 Git、Go 1.26 或更高版本，以及 PostgreSQL 16 或更高版本。

```sh
git clone https://github.com/onprs/ByebyeOCS-Tiku.git
cd ByebyeOCS-Tiku
go build -o ocs-tiku-fork ./cmd/ocs-tiku-fork
cp ocs-tiku-fork.example.json ocs-tiku-fork.json
```

为本服务建立独立的 PostgreSQL 数据库与角色。编辑 `ocs-tiku-fork.json`，填写数据库连接文件、服务令牌文件及模型配置；私密文件权限设为 `0600`，所在目录设为 `0700`。模型密钥仅保存在本服务，ByebyeOCS 使用模型条目 ID 引用它们。

模型配置示例：

```json
{
  "id": "default-text",
  "display_name": "默认文字模型",
  "protocol": "openai-chat",
  "base_url": "https://models.example.com/v1",
  "model": "<供应商模型名称>",
  "api_key_file": "secrets/model.key",
  "max_output_tokens": 2048
}
```

将模型加入配置的 `models` 数组，按需设置 `default_model_id`、图片模型及缓存期限，然后检查配置、显式迁移并启动：

```sh
./ocs-tiku-fork config check -config ocs-tiku-fork.json
./ocs-tiku-fork migrate -config ocs-tiku-fork.json
./ocs-tiku-fork serve -config ocs-tiku-fork.json
```

默认监听 `127.0.0.1:8093`。服务启动不会自动迁移或创建默认模型。访问接口需要服务令牌；ByebyeOCS 中的题库地址和令牌应与此处一致。

## 使用

ByebyeOCS 管理员发布模型条目后，用户可在学习安排中选择模型和作答模式。直接集成使用 `POST /api/v2/answers`，携带 `Authorization: Bearer <服务令牌>` 与稳定的 `Idempotency-Key`。

```json
{
  "request_id": "<唯一请求编号>",
  "model_offering_id": "default-text",
  "mode": "cache_first",
  "cache_scope": "user",
  "cache_scope_key": "<用户范围键>",
  "question": {
    "title": "<题目内容>",
    "options": "",
    "type": "shortanswer",
    "images": []
  },
  "budget": { "max_output_tokens": 1024 }
}
```

缓存只匹配规范化后完全一致的题目、选项、图片与模型条件。返回 `usage_incomplete` 时，上游用量尚不完整，应保留查询结果并停止自动扣费。

答案和精确缓存默认保留7天，token调用明细保留30天。幂等结果到期后，同键返回`request_unknown`，原请求的token汇总仍可核对。

本服务基于 [OCS-Tiku](https://github.com/onprs/OCS-Tiku)。维护与上游同步见 [UPSTREAM.md](UPSTREAM.md)，许可证见 [LICENSE](LICENSE)。
