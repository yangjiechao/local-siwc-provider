# Local SIWC Provider（本地网页控制台）

> **⚠️ 重要合规风险提示（2026 年 11 月 4 日生效的 SIWC 条款）：** 本项目开源不代表允许其他人将 ChatGPT 订阅额度转为**任意第三方工具可调用的通用 API**。OpenAI SIWC 条款 §3.1 要求套餐额度仅用于用户实际授权连接的应用。不得跨用户共享、代替他人发起请求、账号池/轮换账号绕过限额、转售，后台自动化须取得用户明确同意。即使 Harness 和多个 Agent 属于同一个应用，也应自行验证是否符合授权范围；不能仅凭“同一应用”的声明保证合规。违规可能导致应用 SIWC 权限受限或停用。详见 [合规与安全声明](NOTICE-SIWC-COMPLIANCE.md) 和 [OpenAI SIWC 官方条款](https://openai.com/policies/sign-in-with-chatgpt-terms/)。
>
> **独立模型商单独付费：** DeepSeek、火山方舟等通过各自正规 API Key 调用，按对应服务条款和价格计费。发生 SIWC 登录失败、限额或不具资格时，程序不会自动切换到付费供应商。外部请求内容会传给对应模型商，请注意隐私与成本。


> **开发预览版，不是正式可用承诺。** Provider 的 OAuth 授权、SIWC 订阅资格、模型清单和多轮工具调用仍需在真实 ChatGPT 账号上进行端到端验证。开源本身也不代表已获准为任意第三方闭源 Agent 中转 ChatGPT 订阅额度。

只需启动一个本地 Go 程序，就可以在网页中完成：

- **ChatGPT 登录**：点击按钮打开 OpenAI 官方 OAuth 授权页面，无需输入密码到 Provider；授权成功后显示账号和 Token 到期时间。
- **启动 / 停止 Agent API**：一个按钮控制 Agent 接口，网页管理界面始终保持运行。
- **查看 / 复制本地 API Key**：只有解锁的本地控制台才能获取，默认隐藏。**它不是 ChatGPT Token**。
- **查询 / 搜索 / 复制可用模型**：页面列出当前账号返回的全部模型 ID，支持搜索、单个复制、复制全部、选用模型，不预设可用模型。
- **复制 Agent 配置**：包含 base_url、本地 Key 和所选模型 ID。

## macOS 首次使用：双击启动

1. 安装 Go 1.23 或更高版本：<https://go.dev/dl/>。安装一次即可。
2. 下载仓库并解压，或终端运行：
   ```bash
   git clone https://github.com/yangjiechao/local-siwc-provider.git
   ```
3. 在 Finder 中进入项目目录，双击 **`start.command`**。第一次可能需要右键 → 打开（允许从互联网获取的脚本）。
4. 程序自动编译并打开本地控制台。依次点击“使用 ChatGPT 登录”、“启动 API 服务”、“复制本地 Key”和“刷新模型列表”。刷新后可在下方“当前账号的可用模型”逐项查看完整模型 ID，点击“复制”或“复制全部模型 ID”；点击“选用”也会更新 Agent 配置中的模型下拉框。
5. 保持 Terminal 窗口打开；关闭或停止该进程，会同时停止控制台和 API。

如果双击脚本没有执行权限，可以在终端运行：

```bash
cd local-siwc-provider
chmod +x start.command
./start.command
```

也可以直接执行 `go run . ui --dashboard-port 8787`。程序默认监听 `127.0.0.1:8787`，网页地址为 <http://127.0.0.1:8787/ui/>，**不要把网页作为可公开访问的云服务部署**。

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


## 网页检查更新（GitHub main）

在管理页面的 **“检查更新”** 区域，可查看本地版本、GitHub 最新提交、检查时间和更新状态。
点击“检查 GitHub 更新”时，程序查询 GitHub 官方仓库的 `main` 分支，比较 Git 提交先后关系；发现确实领先于本地的版本后，按钮才会启用。当前仓库尚未采用稳定版 Release 标签，因此这里的“版本”是 **Git 提交号**，并非商用软件的语义化版本号。

**一键更新的要求与流程：**

- 仅支持 GitHub 克隆下来的源码目录，使用 `start.command` 启动并由 Go 编译的 `siwc-provider`。需安装 Git 和 Go 1.23+。直接 `go run .` 或下载单个未标记构建提交的二进制文件，不能安全地一键更新。
- 必须在本仓库官方 `origin` 的 **main** 分支，且工作目录没有未提交修改；本地提交与正在运行的二进制必须匹配。若分支分叉、主机无法连接 GitHub，或存在本地修改，只展示原因，不会强行覆盖。
- 更新时先 `git fetch`，验证新版本是旧版本的快进后，在临时 Git worktree 中运行 `go test ./...`、编译 Go 程序；测试或编译失败不会替换当前程序。
- 测试成功后快进源码，备份旧二进制为 `siwc-provider.previous`，安装新程序并重启服务。浏览器会再次打开本地管理页面；正在执行的 Agent 请求可能中断。
- OAuth 账号、API Key、端口和局域网配置保留在用户目录 `~/.config/local-siwc-provider/state.json`，不会因源码更新被清除。
- 自动更新仅在 **本机控制台的授权会话** 操作，局域网 Agent API 不提供更新功能。此功能不可用于公网暴露的网页管理系统。Windows 运行中替换可执行文件暂不支持。

首次获得此功能，需要先手动运行一次 `git pull origin main`，再双击 `start.command`，让本地二进制包含对应提交号。以后可在网页中检查和安装新版本。

若 GitHub 发布了新提交但你本地已经修改过源码，可以先 `git status` 检查，并自行处理修改后再使用更新功能。不要通过强制重置覆盖自己的代码。

## 多模型商转发（预览）

在管理网页的「**额外模型商管理**」中输入模型商 ID、显示名称、接口类型、API Base URL、该供应商自己申请的 API Key、启用状态，以及可选的手动模型 ID（每行一个）。配置只保存在本机 `0600` 保护的状态文件中，网页不会回显供应商原始密钥。启动后程序会后台尝试获取 SIWC 和所有已启用模型商的模型列表，页面也可手动刷新。若供应商不提供标准 `/models`，可手动添加模型；页面会显示目录查询失败原因。

Agent 的 `base_url` 仍然是 `http://127.0.0.1:8087/v1`。模型 ID 推荐 `供应商ID/真实模型ID`，例如 `deepseek/deepseek-flash`；程序会在发送给 DeepSeek 时去掉 `deepseek/` 前缀并使用 DeepSeek 自己的 API Key。`GET /v1/models` 返回各模型商的合并列表。使用不带前缀的模型时，SIWC 模型优先；外部同名模型需要显式指定供应商。不因 SIWC 429、401 或超额而自动转发到收费模型，避免意外账单。

- **DeepSeek 等 OpenAI 兼容商**：支持 `/v1/chat/completions`，若上游支持也可使用 `/v1/responses`；兼容程度以具体厂商为准。参考配置：URL 为 `https://api.deepseek.com`，类型 `openai-compatible`。
- **Seedance / 火山方舟视频模型**：请选择 `ark-video`，Base URL 为 `https://ark.cn-beijing.volces.com/api/v3`（需按自己的地区和官方接口确认），独立 Ark API Key。视频生成不是聊天补全，要通过本地 `POST /v1/contents/generations/tasks` 发起异步任务、通过 `GET /v1/contents/generations/tasks/{id}?provider=供应商ID` 查询。对于未在 `/models` 中返回的 Seedance/Endpoint ID 可手动添加。
- **安全**：模型商只能配置外部 HTTPS 域名，不允许回环/内网地址和跳转。局域网 Agent API 若使用普通 HTTP，API Key、Prompt 和输出未加密；不要映射到公网。外部模型商可能收取费用并处理发送的用户内容。

**给开源使用者的提醒：** 仅限同一被授权应用自身使用 SIWC 的 ChatGPT 套餐；如果将本项目连接到其他无关 Agent、公开出售订阅额度或为他人转发推理，就可能违反 SIWC 的“Connected application only”等要求。请务必阅读 [NOTICE-SIWC-COMPLIANCE.md](NOTICE-SIWC-COMPLIANCE.md) 并自行评估、必要时联系 OpenAI 确认。

**参数兼容提醒：** 调用 SIWC 时可携带 `max_output_tokens`，Provider 会删除此字段，不会因此报错，但输出 Token 上限**不会实际生效**。发送给独立模型商时，除去路由前缀外不会自动剥离此字段，具体由该供应商决定是否支持。
