# OCS 题库

本地运行的 OCS 大模型题库服务。接收题目与选项，调用已配置的模型返回答案，并在网页中查看请求记录、统计和模型设置。

## 安装与启动

需要 Git、Go 1.25 或更高版本、Node.js 22.12 或更高版本及 npm。

```sh
git clone https://github.com/onprs/OCS-Tiku.git
cd OCS-Tiku
cd frontend
npm ci
npm run build
cd ..
go run .
```

在浏览器中打开 http://127.0.0.1:8000 。首次启动会在当前目录创建 `config.yaml` 和 `ocs-tiku.db`。

## 配置

在「系统设置 · 模型服务」中添加模型服务。常用厂商（OpenAI、Anthropic、Google、DeepSeek、Kimi、智谱、通义千问、火山方舟、硅基流动、Ollama 等）已内置端点，填入密钥即可；其他服务可自选协议并填写地址。

在「答题模型」中选择回答模型。模型能力会自动识别，无法识别时可手动标注。图片处理方式由此决定：

- 回答模型支持图片：题目图片直接交给回答模型
- 回答模型不支持图片：由图片转述模型先把图片转换为文字描述
- 两者都不具备：含图片的题目返回错误

每张模型卡片都可以用「测试连通性」检查配置。模型能力数据来自 models.dev，可在页面底部更新。

在「数据看板」复制 OCS 题库配置，粘贴到 OCS 脚本的题库配置中。服务默认仅监听本机，使用期间保持终端运行；按 Ctrl+C 停止。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `OCS_PORT` | 监听端口 |
| `OCS_ANSWER_SERVICE` / `OCS_ANSWER_MODEL` | 回答模型的服务标识与模型名称 |
| `OCS_ANSWER_API_KEY` / `OCS_ANSWER_BASE_URL` | 回答模型所用服务的密钥与地址 |
| `OCS_CAPTION_SERVICE` / `OCS_CAPTION_MODEL` | 图片转述模型的服务标识与模型名称 |
| `OCS_CAPTION_API_KEY` / `OCS_CAPTION_BASE_URL` | 图片转述模型所用服务的密钥与地址 |
