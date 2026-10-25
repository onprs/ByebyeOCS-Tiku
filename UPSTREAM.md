# 上游同步与维护

本文件面向 fork 维护者。公开安装和使用见 [README.md](README.md)。

## 来源与基线

- 上游仓库：<https://github.com/onprs/OCS-Tiku.git>
- 上游基线提交：`aedb64fb0b55b633362558c627887d0f1f4de565`
- 基线提交标题：`fix: 接入 OCS 图片题优化与 Base64 图片处理`
- 本仓库保留完整上游提交历史。
- `upstream` remote 指向上述上游仓库；`origin` 指向 `https://github.com/onprs/ByebyeOCS-Tiku.git`。
- 维护克隆首次设置上游：`git remote add upstream https://github.com/onprs/OCS-Tiku.git`。

## 保留与新增

上游根目录文件保持原布局，便于直接合并上游修复：

- 上游 v1 本地服务：根包 `main`、SQLite、网页、历史检索、Agent 模式仍保留。
- 共享核心：`internal/engine/` 从上游模型客户端、图片处理、答案抽取、能力画像和用量回调提取；v1 与 platform v2 都调用它。
- platform v2：`internal/platform/`、`cmd/ocs-tiku-fork/` 提供 PostgreSQL 题库服务；正式路径不导入 SQLite。
- 正式维护面板：`internal/platform/panel/`，Go 内嵌独立资源与 `/panel/api/`，直接读取正式 PostgreSQL；v1 React 页面继续保留为本地兼容入口。
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
6. 确认 `AGENTS.md`、私密配置和生成产物没有重新进入当前追踪树。
7. 评审与检查通过后提交，并推送 `origin` 中的维护分支。

只向 `origin` 推送本 fork 的更新，保留 `upstream` 的原有历史。

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

## 正式维护面板与缓存策略

正式面板默认关闭，启用后使用配置中独立维护账号登录；`maintainer` 可发布/编辑/下架公开条目、维护价格版本与缓存策略、启停和测试模型、使缓存失效，`viewer` 只读。密码以私密文件中的 bcrypt 哈希配置，使用 `panel-password` 从标准输入或私密文件生成。面板采用 HttpOnly、SameSite=Strict Cookie 和 CSRF 校验，写操作校验精确来源；服务令牌不能用于面板登录，也不会返回给浏览器。会话保存在进程内，8小时到期或重启后失效；登录尝试与会话总数有界。

面板设置只读展示公开运行参数、条目数量与凭据是否配置。模型端点和密钥仍由服务配置文件管理；公开条目、token 价格版本与缓存策略保存在题库库并由面板维护；钱包、预占与结算仍由 ByebyeOCS 管理。模型启停与缓存失效操作和维护审计在同一事务提交；测试前先记录操作，功能测试必须显式确认模型用量。审计默认保留180天，每批清理500行。

新增迁移 `000004_panel_audit.sql` 与 `000005_offerings.sql` 必须在启动前显式执行；旧迁移不变。主平台通过服务令牌读取 `GET /internal/v1/offerings`，公开条目 UUID 与公开编号保持稳定。面板资源随正式二进制构建，不依赖 Node。真实浏览器脚本位于 `internal/platform/panel/tests/browser.test.mjs`，使用 Node.js 22或更高版本与 Chrome/Chromium，仅接收隔离夹具的回环地址。主项目的 `integration,tiku_fork` 测试可通过 `BYEBYEOCS_TIKU_PANEL_PROBE_SCRIPT` 调用该脚本；`BYEBYEOCS_TIKU_BROWSER_ARTIFACT_DIR` 可指定本地截图与报告目录。

v2 请求新增可选 `cache_policy: {allow, ttl_days}`。未提供时保留旧请求格式与默认策略；提供时期限必须在1到配置上限之内。新策略按期限分开缓存，过期或超龄条目不命中；`allow=false` 禁止缓存写入并拒绝 `cache_first`。主平台把请求策略写入查询快照，旧在途行的 NULL 仍按旧协议恢复，避免恢复时改变 fork 的幂等摘要。部署时主平台与 fork 配套升级。

v1 本地兼容进程限制回环监听，管理 API 拒绝非本机 Host 和跨站来源；搜索接口保持兼容。设置返回的模型密钥遮蔽，编辑留空仅在端点和协议未变时保留旧凭据，显式移除使用 `clear_api_key`。

## 发布授权

- `.github/workflows/release.yml` 只保留手动触发，推送标签不会自动发布。
- 手动运行填写 `tag` 并将 `publish` 设为 `true`；维护者可在 GitHub 的 `release` environment 另行配置审核规则。
- 真实发布、签名、商店上架和外部账号配置不属于本工作区的默认验证，也不能用本地夹具成功代替。

## 公开构建依赖

- 正式 platform：Go 1.26 或更高版本、PostgreSQL 16。
- v1 网页与单文件发布：Node.js 22、npm；浏览器回归需要 Chrome 或 Chromium。
- 不依赖维护者私有目录、SSH 别名、代理端口或真实模型密钥。

## 已知边界

- 本 fork 使用 [MIT 许可证](LICENSE)，上游来源、历史和第三方许可声明分别保留。
- 上游根包仍使用 SQLite，这是 v1 本地兼容路径，不是正式 platform 存储。
- OpenAI 兼容响应通过 raw body 判断 `usage` 是否存在；其他协议依赖 provider 返回的 metadata，并校验 `total = input + output`。缺失或矛盾时标记 `usage_incomplete`，不自动计费。
- 上游网页的单文件行为需要 `ocs_frontend` 标签；默认构建是目录模式。
- fork 的 v2 不实现上游模糊历史检索作为缓存；相似题不能替代精确缓存。
