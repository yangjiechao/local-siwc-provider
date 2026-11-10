# Local SIWC Provider（本地网页控制台）

> **开发预览版，不是正式可用承诺。** Provider 的 OAuth 授权、SIWC 订阅资格、模型清单和多轮工具调用仍需在真实 ChatGPT 账号上进行端到端验证。开源本身也不代表已获准为任意第三方闭源 Agent 中转 ChatGPT 订阅额度。

只需启动一个本地 Go 程序，就可以在网页中完成：

- **ChatGPT 登录**：点击按钮打开 OpenAI 官方 OAuth 授权页面，无需输入密码到 Provider；授权成功后显示账号和 Token 到期时间。
- **启动 / 停止 Agent API**：一个按钮控制 Agent 接口，网页管理界面始终保持运行。
- **查看 / 复制本地 API Key**：只有解锁的本地控制台才能获取，默认隐藏。**它不是 ChatGPT Token**。
- **查询 / 选择可用模型**：直接查询当前账号的 SIWC 模型目录，不预设可用模型。
- **复制 Agent 配置**：包含 base_url、本地 Key 和所选模型 ID。

## macOS 首次使用：双击启动

1. 安装 Go 1.23 或更高版本：<https://go.dev/dl/>。安装一次即可。
2. 下载仓库并解压，或终端运行：
   ```bash
   git clone https://github.com/yangjiechao/local-siwc-provider.git
   ```
3. 在 Finder 中进入项目目录，双击 **`start.command`**。第一次可能需要右键 → 打开（允许从互联网获取的脚本）。
4. 程序自动编译并打开本地控制台。依次点击“使用 ChatGPT 登录”、“启动 API 服务”、“复制本地 Key”和“刷新模型列表”。
5. 保持 Terminal 窗口打开；关闭或停止该进程，会同时停止控制台和 API。

如果双击脚本没有执行权限，可以在终端运行：

```bash
cd local-siwc-provider
chmod +x start.command
./start.command
```

也可以直接执行 `go run . ui --port 8787`。程序默认监听 `127.0.0.1:8787`，网页地址为 <http://127.0.0.1:8787/ui/>，**不要把网页作为可公开访问的云服务部署**。

### 连接你的工具 Agent

在你的 Agent 设置中配置：

```yaml
base_url: http://127.0.0.1:8087/v1
api_key: <网页中复制的本地API-Key>
model: <网页中查询到的模型ID>
```

Provider 目前提供：`GET /v1/models`、`POST /v1/chat/completions`（部分兼容）、`POST /v1/responses`。部分 SDK 自动带的参数（如 `temperature`、`max_completion_tokens`、`response_format`）当前会被明确拒绝；复杂 Agent 需要额外兼容测试。原生 Responses API 更适合保留高级推理项。

### 安全与状态

- OAuth Token、刷新 Token、两个不同的本地密钥均保存在你的 Mac 上：`~/.config/local-siwc-provider/state.json`。目录权限 `0700`，文件权限 `0600`。
- 网页控制台的解锁密钥 **不同于 Agent API Key**；首次打开由本地启动器经 URL fragment 传到浏览器，并立即从地址栏移除。浏览器使用 HttpOnly、SameSite=Strict Cookie 保持会话，默认八小时到期。
- 管理接口要求同源访问和防 CSRF 请求头；Agent API 仍使用独立的 Bearer Key，拒绝浏览器跨域调用。
- 不要将 `state.json`、任何密钥、包含解锁 fragment 的链接分享或提交到 GitHub。
- 点击“停止 API”后，管理网页仍可操作；你也可以在页面内退出 ChatGPT 授权。
- 保持单实例运行。同一端口已有进程时，需先停止旧进程再启动。

### 命令行替代方式

```bash
go build -o siwc-provider .
./siwc-provider ui --port 8787     # 打开网页控制台，API 初始为停止
./siwc-provider login              # 旧版终端 OAuth 登录
./siwc-provider status             # 查看登录状态
./siwc-provider key                # 终端显示 Agent 本地 Key
./siwc-provider serve --port 8787  # 立即启用 Agent API，附带网页界面
./siwc-provider logout             # 退出授权
```

如果网页已关闭但后端仍在运行，在**同一个浏览器、会话未过期**的情况下可以重新打开本地地址。否则请先停止旧进程，再重新运行 `ui` 解锁。

官方参考文档：[SIWC Quickstart](https://developers.openai.com/siwc/quickstart)、[Registration](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)、[Models & Inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)。
