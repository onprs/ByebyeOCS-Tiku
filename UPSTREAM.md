# 上游同步与维护

本文件面向 fork 维护者。公开安装和使用见 [README.md](README.md)。

## 来源与基线

- 上游仓库：<https://github.com/onprs/OCS-Tiku.git>
- 上游基线提交：`aedb64fb0b55b633362558c627887d0f1f4de565`
- 基线提交标题：`fix: 接入 OCS 图片题优化与 Base64 图片处理`
- 本仓库由相邻上游工作区执行 `git clone --no-hardlinks` 得到，保留完整上游提交历史。
- `upstream` remote 指向上述上游仓库；`origin` 预期指向 `https://github.com/onprs/ByebyeOCS-Tiku.git`。仓库创建、推送和 submodule 指向由协调器完成。

## 保留与新增

上游根目录文件保持原布局，便于直接合并上游修复：

- 上游 v1 本地服务：根包 `main`、SQLite、网页、历史检索、Agent 模式仍保留。
- 共享核心：`internal/engine/` 从上游客服端、图片处理、答案抽取、能力画像和用量回调提取；v1 与 platform v2 都调用它。
- platform v2：`internal/platform/`、`cmd/ocs-tiku-fork/` 提供 PostgreSQL 题库服务；正式路径不导入 SQLite。
- v2 扩展由当前 ByebyeOCS `services/tiku` 迁移而来，保留 v1 `/api/search`、v2 请求级模型、精确缓存/失效、幂等和资源限制。

已移除仓库根目录 `AGENTS.md`，避免维护约束进入公开树；上游历史中仍保留该文件。`.gitignore` 继续忽略真实配置、密钥、数据库、日志和本机维护目录。

## 上游同步流程

在独立维护克隆中操作，不在生产工作区直接合并：

```sh
git fetch upstream
git log --oneline HEAD..upstream/main
git diff --stat HEAD...upstream/main
git merge upstream/main
```

合并后必须：

1. 检查上游根包与 `internal/engine` 的调用点是否仍然一致。
2. 运行 `gofmt`、`go vet ./...`、`go test ./...`。
3. 用隔离 PostgreSQL 运行 platform 集成测试。
4. 检查 `cmd/ocs-tiku-fork` 的 `config check`、`migrate`、`serve` 参数未被上游改动破坏。
5. 更新本文件的“上游基线提交”并记录合并提交。
6. 只有通过评审和测试后才提交；本工作区不执行 `git push`。

不要向上游 `upstream` 推送，也不要用本 fork 覆盖上游历史。

## 构建与测试

正式 platform 二进制只需要 Go：

```sh
go build ./cmd/ocs-tiku-fork
go test ./...
go vet ./...
```

PostgreSQL 集成测试需要独立测试实例：

```sh
BYEBYEOCS_TIKU_TEST_DATABASE_DSN_FILE=/path/to/test-admin.dsn \
  go test ./...
```

测试会创建并清理临时数据库，不能指向真实业务实例。模型测试使用本地 TLS provider 夹具，不调用真实付费模型。

v1 网页构建需要 Node.js 22 与 npm：

```sh
npm --prefix frontend ci
npm --prefix frontend run build
go test -tags ocs_frontend .
```

发布单文件 v1 可执行文件使用 `-tags ocs_frontend`；`scripts/build-release.sh` 已包含该标签。默认构建从 `frontend/dist` 读取网页，不嵌入资源，因此主仓库的 Go 测试不需要 Node。

## 发布授权

- `.github/workflows/release.yml` 只保留手动触发，推送标签不会自动发布。
- 手动运行必须填写 `tag`，并且显式将 `publish` 设为 `true`；发布任务还使用 GitHub `release` environment 进行人工授权。
- 真实发布、签名、商店上架和外部账号配置不属于本工作区的默认验证，也不能用本地夹具成功代替。

## 公开构建依赖

- 正式 platform：Go 1.26 或更高版本、PostgreSQL 16。
- v1 网页与单文件发布：Node.js 22、npm；浏览器回归需要 Chrome 或 Chromium。
- 不依赖维护者私有目录、SSH 别名、代理端口或真实模型密钥。

## 已知边界

- 上游没有 LICENSE 文件；公开分发前需要所有者补充许可证并核对依赖许可。
- 上游根包仍使用 SQLite，这是 v1 本地兼容路径，不是正式 platform 存储。
- OpenAI 兼容响应通过 raw body 判断 `usage` 是否存在；其他协议依赖 provider 返回的 metadata，并校验 `total = input + output`。缺失或矛盾时标记 `usage_incomplete`，不自动计费。
- 上游网页的单文件行为需要 `ocs_frontend` 标签；默认构建是目录模式。
- fork 的 v2 不实现上游模糊历史检索作为缓存；相似题不能替代精确缓存。
