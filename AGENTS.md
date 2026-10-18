# OCS-Tiku 开发维护约定

本目录是 OCS-Tiku 的 Git 根目录。项目仓库为 https://github.com/onprs/OCS-Tiku.git ，默认开发分支为 `main`。

## 上游参考

- OCS 网课助手官方代码仓库：https://github.com/ocsjs/ocsjs 。官方文档：https://docs.ocsjs.com/ 。修改 OCS 题库配置、脚本对接和请求格式时，先对照对应版本的官方仓库和文档，再验证本项目接口；不要仅凭旧实现推断当前协议。
- OCS 官方仓库用于参考实现，不能作为本项目的推送目标。`origin` 应指向本项目仓库。

## 项目结构与验证

- `main.go` 启动 Go 服务；`server.go` 提供题库搜索、配置、记录与静态页面接口；`solver.go`、`preprocess.go`、`model.go` 处理答题与图片；`providers.go` 按协议构造模型客户端；`capability.go` 合并模型库推断与手动标注；`services.go` 定义预置服务模板；`store.go` 使用 SQLite；`history.go` 维护历史检索倒排索引与升级迁移；`config.go` 读取和保存配置。
- `internal/catalog` 维护模型能力索引：内置 models.dev 快照、本地缓存与 ETag 条件更新；`tools/snapshot` 生成内置快照。
- `frontend/` 是 React、TypeScript、Vite 应用；生产构建输出到 `frontend/dist/`，由 `frontend.go` 嵌入可执行文件。运行 Go 构建、测试或静态检查前，先运行 `npm --prefix frontend ci` 和 `npm --prefix frontend run build`。`legacy/python/` 是旧实现归档，不参与新版启动。
- 后端运行 `go test . ./internal/catalog ./tools/snapshot` 和 `go vet . ./internal/catalog ./tools/snapshot`；前端运行 `npm run build`、`npm run lint`；浏览器回归在构建后运行 `npm run test:browser`，需要 Chrome 或 Chromium，可用 `CHROME_BIN` 指定浏览器。涉及行为修改时补充相应测试。检查结果有警告或失败时如实记录。
- 公开安装和使用见 `README.md`。公开说明不能依赖维护者的本机路径、代理或账号。
- 协议与厂商是两个维度：协议决定调用组件与消息编码，厂商决定能力元数据归属与私有扩展，不要混作一个字段。

## 发布维护

- Release 产物为包含网页资源的单个可执行文件，覆盖 Windows、Linux、macOS 的 amd64 和 arm64；发布包不依赖外部网页目录或运行时工具链。
- 本地发布构建在 Linux 维护环境执行，需要 Bash、Go 和 `sha256sum`。先完成前端构建，再运行 `bash scripts/build-release.sh v主版本.次版本.修订版本`，产物及 `SHA256SUMS` 写入 `dist/对应版本/`。
- 使用 `python3 scripts/check-release.py 可执行文件 版本号` 在空目录中检查本机平台的发布文件；该检查只证明当前平台的启动行为。
- 每次发布在 `docs/releases/对应版本.md` 编写面向用户的功能说明。推送版本标签会触发 `.github/workflows/release.yml`，完成检查、构建和单文件启动验证后自动发布 GitHub Release。
- `-frontend-dir` 可在维护时覆盖内置网页；公开用户默认直接运行下载的可执行文件。

## 提交与同步

- 不提交真实 API Key、`.env`、`config.yaml`、数据库、日志、下载图片、证书或本机调试产物。示例配置只使用占位值。
- 提交前检查 `.gitignore`、`git status --short`、`git diff --cached --name-only` 和暂存内容。每批修改完成后在本仓库提交并推送 `origin/main`；先核对远端状态，不强制推送。推送失败则保留本地提交并说明未同步状态。
