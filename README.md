# ByebyeOCS-Tiku

为 [ByebyeOCS](https://github.com/onprs/ByebyeOCS) 提供模型作答、精确答案缓存和 token 用量查询，兼容 OCS 客户端的 `/api/search` 接口。

## 功能

- 支持单选、多选、判断、填空和简答题，以及图片题。
- 按请求选择已配置模型，支持 OpenAI 兼容、Anthropic、Gemini 和 Ollama 协议。
- `cache_first` 优先查精确缓存，未命中再调用模型；`always_llm` 每次调用模型，允许缓存时保留成功答案。
- 返回输入、输出及总 token、缓存命中和调用明细；缓存命中 token 为零，用量不完整时明确标记。
- 维护面板发布公开模型条目、token 价格版本与缓存策略，支持模型启停、连通性检查、功能测试、用量汇总和错误缓存失效。
- 提供带独立登录的维护面板，区分维护员与只读账号；支持石墨、青禾、靛蓝及浅色、深色显示。
- 相同幂等请求返回保留期内的原结果，过期请求保留记录以防重复调用；公开价格版本在此维护，钱包、预占、结算与账本由 ByebyeOCS 管理。

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

将模型加入配置的 `models` 数组，按需设置 `default_model_id`（仅 v1 兼容入口使用）、图片模型及缓存期限，然后检查配置、显式迁移并启动：

```sh
./ocs-tiku-fork config check -config ocs-tiku-fork.json
./ocs-tiku-fork migrate -config ocs-tiku-fork.json
./ocs-tiku-fork serve -config ocs-tiku-fork.json
```

默认监听 `127.0.0.1:8093`。启动前需显式完成对应版本迁移，未迁移或版本不匹配时服务会退出；服务不会创建默认模型。访问接口需要服务令牌；ByebyeOCS 中的题库地址和令牌应与此处一致。可用性检查使用`/readyz`，存活检查使用`/healthz`。

## 使用

在维护面板发布公开条目后，ByebyeOCS 用户可在学习安排中选择模型和作答模式。主平台通过服务令牌读取 `GET /internal/v1/offerings` 获取目录。直接集成使用 `POST /api/v2/answers`，携带 `Authorization: Bearer <服务令牌>` 与稳定的 `Idempotency-Key`。

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
  "budget": { "max_output_tokens": 1024 },
  "cache_policy": { "allow": true, "ttl_days": 7 }
}
```

缓存只匹配规范化后完全一致的题目、选项、图片与模型条件。返回 `usage_incomplete` 时，上游用量尚不完整，应保留查询结果并停止自动扣费。

精确缓存按请求的 `cache_policy.ttl_days` 保存，期限不能超过 `cache.max_ttl_days`；未提供策略时使用服务默认值。`allow: false` 配合 `always_llm` 可禁止保存答案缓存。查询答案默认保留7天，token调用明细保留30天。幂等结果到期后，同键返回`request_unknown`，原请求的token汇总仍可核对。

## 维护面板

面板提供运行总览、模型与价格、查询详情、精确缓存、操作记录和外观设置。维护员可发布、编辑、下架公开条目，维护价格版本、默认项与缓存策略，启停和测试模型，并使错误缓存失效；只读账号仅可查看。公开条目、价格与缓存策略只在此面板维护；模型密钥与内部服务令牌不会显示在面板中。

面板默认关闭。可在 Bash 中为维护账号生成密码哈希：

```bash
mkdir -p secrets
chmod 0700 secrets
read -r -s -p '维护密码：' TIKU_MAINTENANCE_PASSWORD
printf '\n'
printf '%s' "$TIKU_MAINTENANCE_PASSWORD" | ./ocs-tiku-fork panel-password -out secrets/maintainer.hash
unset TIKU_MAINTENANCE_PASSWORD
```

密码至少12个字符且不超过72字节。将以下配置加入 `ocs-tiku-fork.json`：

```json
"panel": {
  "enabled": true,
  "origin": "http://127.0.0.1:8093",
  "users": [
    {
      "username": "maintainer",
      "password_hash_file": "secrets/maintainer.hash",
      "role": "maintainer"
    }
  ]
}
```

检查配置并重启服务后，访问 `http://127.0.0.1:8093/panel/`，使用配置中的账号和刚设置的密码登录。只读账号使用独立的密码哈希文件并将 `role` 设为 `viewer`。

远程访问需通过 HTTPS 入口，并将 `panel.origin` 设为浏览器实际访问的来源地址，例如主管理端同域挂载时填 `https://admin.example.com`，不带 `/panel/` 路径。面板的登录账号与接口使用的服务令牌分别配置。

本服务基于 [OCS-Tiku](https://github.com/onprs/OCS-Tiku)。维护与上游同步见 [UPSTREAM.md](UPSTREAM.md)，许可证见 [LICENSE](LICENSE)。
