# OCS-Tiku 开发维护约定

本目录是 OCS-Tiku 的 Git 根目录。项目仓库为 https://github.com/onprs/OCS-Tiku.git ，默认开发分支为 `main`。

## 上游参考

- OCS 网课助手官方代码仓库：https://github.com/ocsjs/ocsjs 。官方文档：https://docs.ocsjs.com/ 。修改 OCS 题库配置、脚本对接和请求格式时，先对照对应版本的官方仓库和文档，再验证本项目接口；不要仅凭旧实现推断当前协议。
- OCS 官方仓库用于参考实现，不能作为本项目的推送目标。`origin` 应指向本项目仓库。

## 项目结构与验证

- `main.go` 启动 Go 服务；`server.go` 提供题库搜索、配置、记录与静态页面接口；`solver.go`、`preprocess.go`、`model.go` 处理答题与图片；`providers.go` 按协议构造模型客户端；`capability.go` 合并模型库推断与手动标注；`services.go` 定义预置服务模板；`store.go` 使用 SQLite；`history.go` 维护历史检索倒排索引与升级迁移；`config.go` 读取和保存配置。
- `internal/catalog` 维护模型能力索引：内置 models.dev 快照、本地缓存与 ETag 条件更新；`tools/snapshot` 生成内置快照。
- `frontend/` 是 React、TypeScript、Vite 应用；生产构建输出到 `frontend/dist/`。`legacy/python/` 是旧实现归档，不参与新版启动。
- 后端运行 `go test . ./internal/catalog ./tools/snapshot` 和 `go vet . ./internal/catalog ./tools/snapshot`；前端运行 `npm run build`、`npm run lint`；浏览器回归在构建后运行 `npm run test:browser`，需要 Chrome 或 Chromium，可用 `CHROME_BIN` 指定浏览器。涉及行为修改时补充相应测试。检查结果有警告或失败时如实记录。
- 公开安装和使用见 `README.md`。公开说明不能依赖维护者的本机路径、代理或账号。
- 协议与厂商是两个维度：协议决定调用组件与消息编码，厂商决定能力元数据归属与私有扩展，不要混作一个字段。

## 提交与同步

- 不提交真实 API Key、`.env`、`config.yaml`、数据库、日志、下载图片、证书或本机调试产物。示例配置只使用占位值。
- 提交前检查 `.gitignore`、`git status --short`、`git diff --cached --name-only` 和暂存内容。每批修改完成后在本仓库提交并推送 `origin/main`；先核对远端状态，不强制推送。推送失败则保留本地提交并说明未同步状态。
