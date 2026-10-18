# OCS 题库

本地运行的 OCS 大模型题库服务。接收题目与选项，调用已配置的模型返回答案，并在网页中查看请求记录、统计和模型设置。

## 安装与启动

从 [Releases](https://github.com/onprs/OCS-Tiku/releases/latest) 下载对应系统与架构的可执行文件：Windows 选择 `.exe`，Linux 选择 `linux`，macOS 选择 `darwin`；x64 选择 `amd64`，ARM64 选择 `arm64`。无需安装 Go 或 Node.js。

将文件放入一个可写目录，并重命名为 `ocs-tiku.exe`（Windows）或 `ocs-tiku`（Linux/macOS），在该目录打开终端启动。

Windows（PowerShell）：

```powershell
.\ocs-tiku.exe
```

Linux / macOS：

```sh
chmod +x ocs-tiku
./ocs-tiku
```

在浏览器中打开 http://127.0.0.1:8000 。首次启动会在当前目录创建 `config.yaml` 和 `ocs-tiku.db`。

### 从源码启动

需要 Git、Go 1.25 或更高版本、Node.js 22.12 或更高版本及 npm。

```sh
git clone https://github.com/onprs/OCS-Tiku.git
cd OCS-Tiku
npm --prefix frontend ci
npm --prefix frontend run build
go run .
```

## 配置

在「系统设置 · 模型服务」中添加模型服务。常用厂商（OpenAI、Anthropic、Google、DeepSeek、Kimi、智谱、通义千问、火山方舟、硅基流动、Ollama 等）已内置端点，填入密钥即可；其他服务可自选协议并填写地址。

在「答题模型」中选择回答模型。模型能力会自动识别，无法识别时可手动标注。图片处理方式由此决定：

- 回答模型支持图片：题目图片直接交给回答模型
- 回答模型不支持图片：由图片转述模型先把图片转换为文字描述
- 两者都不具备：含图片的题目返回错误

每张模型卡片都可以用「测试连通性」检查配置。模型能力数据来自 models.dev，可在页面底部更新。

「答题设置」中可以开启 Agent 模式。开启后模型会分步推理，并使用历史题目检索工具辅助作答，适合需要多步确认的题目；该模式要求回答模型支持工具调用，作答失败时会自动回退到普通方式。

每条记录的详情会列出本次答题的模型调用次数、Token 用量与耗时，数据看板汇总当日与累计用量。

在「数据看板」复制 OCS 题库配置，粘贴到 OCS 脚本的题库配置中。服务默认仅监听本机，使用期间保持终端运行；按 Ctrl+C 停止。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `OCS_PORT` | 监听端口 |
| `OCS_ANSWER_SERVICE` / `OCS_ANSWER_MODEL` | 回答模型的服务标识与模型名称 |
| `OCS_ANSWER_API_KEY` / `OCS_ANSWER_BASE_URL` | 回答模型所用服务的密钥与地址 |
| `OCS_CAPTION_SERVICE` / `OCS_CAPTION_MODEL` | 图片转述模型的服务标识与模型名称 |
| `OCS_CAPTION_API_KEY` / `OCS_CAPTION_BASE_URL` | 图片转述模型所用服务的密钥与地址 |
