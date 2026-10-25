# 上游兼容前端维护

此目录供根级上游兼容服务使用。根级服务的 `frontend.go`、`frontend_embedded.go` 和 `scripts/build-release.sh` 仍引用本目录，分别读取或内嵌 `frontend/dist`。

ByebyeOCS 使用 `cmd/ocs-tiku-fork`，其正式维护入口为 `/panel/`，资源位于 `internal/platform/panel/assets/`，直接随 Go 程序内嵌。构建本目录不会更新该维护入口。

在独立题库仓库根目录执行兼容前端的开发维护命令：

```sh
npm --prefix frontend ci
npm --prefix frontend run build
npm --prefix frontend run lint
npm --prefix frontend run test:browser
```

兼容前端的浏览器检查需要本机 Chrome 或 Chromium。正式 fork 的安装与使用见根目录 [README](../README.md)。
