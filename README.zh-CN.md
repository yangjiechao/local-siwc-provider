# Local SIWC Provider（本地网页控制台）

> **开发预览版**：OAuth 及 SIWC 套餐资格、模型调用和多轮工具调用需要在真实账号测试。开源本身不意味着任意闭源 Agent 均获准中转使用订阅额度。

这个开源 Provider 可以在本机网页管理：

- **使用 ChatGPT 登录**：通过官方 OAuth 页面授权，成功后查看登录账号和 Token 到期时间。
- **启停本地 API**：API 与网页管理分开控制，关闭 API 不会关闭网页。
- **本地 API Key**：独立于 OAuth Token，默认隐藏，可在管理页面显示、复制。
- **查询可用模型**：点击按钮从账号实际可用的 SIWC 模型目录获取列表。
- **复制 Agent 配置**：包含 base_url、本地 Key 和选中的模型 ID。

## 一键启动（macOS）

1. 安装 [Go 1.23+](https://go.dev/dl/)（仅第一次构建需要）。
2. 执行 `git clone https://github.com/yangjiechao/local-siwc-provider.git` 下载项目。
3. 在 Finder 中双击 `start.command`。如果被 macOS 拦截，可右键选择“打开”；如无执行权限，先执行 `chmod +x start.command`。
4. 浏览器会打开 **本机控制台**。依次点击“使用 ChatGPT 登录”、“启动 API 服务”、“复制本地 Key”和“刷新模型列表”。
5. 保持启动器的终端窗口运行。关闭进程会同时停止控制台和 API。

也可以通过终端启动：

```bash
go run . ui --port 8787
```

网页地址为 `http://127.0.0.1:8787/ui/`。仅本机可访问，不要部署为公网或共享服务器。

## Agent 接入

```yaml
base_url: http://127.0.0.1:8787/v1
api_key: <网页中复制的本地Key>
model: <页面查询到的真实模型ID>
```

Provider 实现了 `GET /v1/models`、`POST /v1/chat/completions`（部分兼容）和 `POST /v1/responses`。部分 SDK 自动添加的参数例如 `temperature`、`max_completion_tokens`、`response_format` 暂时不支持，会明确返回错误；多 Agent 场景需要逐项测试。

## 安全提示

- 账号凭据及本地密钥储存在 `~/.config/local-siwc-provider/state.json`（目录 `0700`，文件 `0600`）。
- 网页管理使用一个独立的管理员密钥，和 Agent 的 API Key 不同；首次打开由本机启动器通过 URL fragment 解锁，随后浏览器使用 HttpOnly、SameSite=Strict Cookie。
- 不要共享任何密钥、`state.json`、包含解锁片段的链接，也不要上传到 GitHub。
- 一个状态文件只运行一个实例；网页关闭但后台仍在运行时，可在会话未过期的浏览器重新打开页面，否则先停止后重新启动。

原有命令仍然可用：`./siwc-provider login`、`status`、`key`、`serve --port 8787`、`logout`。

官方文档：[SIWC Quickstart](https://developers.openai.com/siwc/quickstart)、[Sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)、[Models](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)。
