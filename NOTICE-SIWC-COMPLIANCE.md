# SIWC authorization, compliance and security notice / 合规与安全声明

> **Important: publication, installation and OAuth success do not establish compliance with a particular use.** This is a technical reference, not legal advice or an official OpenAI approval.

## English

### Scope of ChatGPT plan authorization

The [OpenAI Sign in with ChatGPT Terms](https://openai.com/policies/sign-in-with-chatgpt-terms/) (effective **November 4, 2026**) contain special restrictions on ChatGPT plan usage in §3.1:

1. **Connected application only.** A user's plan may only power the app they connected to SIWC. Do **not** expose their subscription as a generic OpenAI-compatible API for unrelated, independent apps or tools. Merely open-sourcing a proxy does not grant an exception.
2. **The right user, the right request.** Calls must be for the authenticated user's own activity or expressly authorized automation. Another person must not trigger the owner's requests. Obtain the user's explicit consent before background automation.
3. **No pooling or resale.** Never create/rotate multiple ChatGPT accounts to increase quotas; do not pool, share, transfer, resell, gift, or reallocate subscription allowances or authentication tokens.
4. **User control and security.** Keep tokens in the user's own local runtime, protect saved credentials and local network connections, and do not impersonate an official OpenAI client.
5. **No paid access to plan usage.** SIWC plan usage must be available without users paying the application developer or upgrading to a paid app. Ordinary plan limits and eligibility continue to apply; violation can lead to SIWC suspension.

A user's internal multi-agent subsystem **may** form part of the same connected app, but that is not an automatic approval. Developers distributing this library must establish the actual authorization boundary and should seek OpenAI guidance for ambiguous integrations. Do not market the adapter as a way to convert personal subscriptions into APIs for unrelated apps.

**Images API isolation:** `/v1/images/generations` and `/v1/images/edits` are only available to explicitly configured external providers using their own API keys and billing. They never use SIWC plan tokens or switch provider automatically. Uploaded images/masks and prompts are sent to the selected vendor; LAN HTTP does not encrypt them.
### Independent external providers

External-provider API keys are **not** ChatGPT tokens. OpenAI API, DeepSeek, Volcengine Ark and other vendors may have independent API agreements, usage caps and charges. Route only the models explicitly configured by the operator. Never treat SIWC 401/403/429 as permission for an invisible charged fallback, and never reuse SIWC OAuth tokens with other vendors. Provider prompts/attachments are sent to the selected third party under that provider's privacy terms.

### Security and delivery risks

The Agent API can be enabled for private LAN clients. LAN HTTP is unencrypted: API keys and prompts can be intercepted. Keep dashboard management loopback-only; never expose the API to the public Internet, and use a secure tunnel/HTTPS if deployment needs more than a trusted LAN. Provider keys are stored in owner-readable `0600` local state and deliberately never returned by the dashboard provider listing. Each user must authorize **their own** ChatGPT account; publishing source code never shares the original author's tokens. Read upstream terms directly and evaluate the legal requirements in your country.

## 中文

### ChatGPT 套餐使用边界

OpenAI [Sign in with ChatGPT 服务条款](https://openai.com/policies/sign-in-with-chatgpt-terms/) 自 **2026 年 11 月 4 日**生效，其中 §3.1 对套餐额度有明确限制：

1. **只能服务于用户授权连接的应用。** 不得将订阅额度包装成任意第三方工具/无关应用均可调用的通用 API；仅将 Provider 代码开源，并不获得豁免。
2. **仅用于真实授权用户。** 只能响应用户自身活动或其明确授权的自动化/后台流程，后台调用须征得明确同意。不能替其他用户用此账号调用。
3. **不得共享、转卖或绕过限额。** 不得账号池、轮换账号分摊额度，不得转让、出售、赠送账号套餐或 OAuth Token。
4. **本地控制和凭据保护。** Token 仅保留在该用户控制的本地环境，避免公开暴露、伪装官方应用、凭据泄露或私有网络未加密传输。
5. **不得收取额外 SIWC 套餐使用费。** 套餐资格和使用限制依然生效，违规可能导致应用的 SIWC 接入被暂停。

**同一应用内多个 Agent** 可以作为待评估的设计，但“我们是同一应用”并非官方批准证明。开源下载者必须依据自己的实际授权范围评估。如果把本项目用作**不相关第三方软件的通用订阅 API 网关**，就存在明确条款冲突风险。存在疑问时应先向 OpenAI 取得说明，不要以 OAuth 登录成功作为合规依据。

**Images 接口独立计费：** `/v1/images/generations` 与 `/v1/images/edits` 必须选择已配置的外部模型商及其独立 API Key，不能用 SIWC 套餐生图，也不会在失败时自动切换供应商。上传的图片、蒙版及提示词会发送到相应服务商；局域网 HTTP 不加密。
### 额外模型商独立计费

本项目对 DeepSeek、火山方舟、OpenAI 正规 API 等模型商的连接只使用**各自独立 API Key**。各供应商的使用资格、限制、价格和隐私政策需要自行核对。不得将 ChatGPT 的 SIWC OAuth Token 传给第三方；SIWC 请求失败或限额耗尽时也不会自动使用其他供应商产生额外账单。选择外部模型商即代表把相应 Prompt、文件或视频素材发往该供应商，使用前需确认数据处理规则。

### 网络及账户安全

局域网 HTTP 接口未加密，Agent API Key、提示词和响应可能被同网络设备截获，仅在可信网络使用，不得暴露公网。管理页面始终绑定 127.0.0.1。外部模型商密钥仅保存在用户自己的 `0600` 状态文件，列表 API 不回显密钥。每位下载者应当自己通过官方授权流程登录自己的 ChatGPT 账号，**不会自动取得开源作者的账号和额度**。

This document is not legal advice. / 本文不是法律意见。
