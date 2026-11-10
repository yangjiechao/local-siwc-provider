# Local SIWC Provider

> [!CAUTION]
> **SIWC compliance and account-risk notice (effective terms: November 4, 2026):** This open-source code is **not a license to expose ChatGPT plan usage as a general-purpose API gateway**. OpenAI SIWC §3.1 limits plan usage to the application the user explicitly connected. Do **not** use the SIWC route to serve other unrelated apps, pool or resell accounts, fulfill another user's requests, bypass usage limits, or run unconsented background jobs. A user's own multi-agent system may be part of their authorized application, but claiming that fact does **not** itself establish compliance. Verify your specific integration and request authorization where needed. Violations may cause application access to be suspended. See [full compliance and security notice](NOTICE-SIWC-COMPLIANCE.md) and [OpenAI SIWC Terms](https://openai.com/policies/sign-in-with-chatgpt-terms/).
>
> **External providers use separate credentials and billing.** They do not expand SIWC entitlements, and SIWC 401/403/429 errors never trigger fallback to a paid provider. Prompts and media submitted to external APIs are sent to those vendors subject to their own terms and privacy policies.


[简体中文使用指南](README.zh-CN.md) · Dashboard available in Chinese.

**New in 0.3 preview:** Separate localhost dashboard (8787) and Agent API (8087), configurable Agent API port, LAN access and Mac `.local` URL, custom Agent API keys, service restart and full app restart/quit controls.

An **experimental**, single-user, locally administered Go adapter for [Sign in with ChatGPT (SIWC)](https://developers.openai.com/siwc/quickstart) and OpenAI's public **Responses API**. It enables eligible clients to authenticate with ChatGPT and supplies a limited OpenAI-compatible interface for local tools.

> **Preview / not yet live-verified.** The OAuth, function-namespace, SSE, and Chat Completions mapping implementation has local unit tests, but it has **not** been authenticated against a real ChatGPT account or tested with a production Harness. Do not interpret publication or a working OAuth callback as OpenAI approval for all downstream uses. In particular, whether a generic OSS adapter can relay subscription usage to independent private agents is a policy/authorization question that must be confirmed before deployment. No bypass or account pooling is included.

## Scope

- Official SIWC **dynamic open-source client registration** (`dynamic_agent_client` is used only for first registration).
- OAuth authorization code with PKCE S256, `state`, `nonce`, issued `client_id`, and **ID token RS256/JWKS verification**.
- Protected local state file (`0600`) with a stable per-host identifier, OAuth credentials, and a randomly generated Agent API key that can be replaced manually through the dashboard.
- Automatic serialized refresh of an expiring OAuth access token (one running process per state file).
- `GET /v1/models`: translates account-specific SIWC model list to an OpenAI-compatible `data[]` list. The dashboard displays the full returned model IDs, supports filtering, selecting, copying individually, or copying all IDs as newline-separated text.
- `POST /v1/responses`: forwards supported requests; always enforces upstream `store:false` and `stream:true`. Supports streaming downstream or aggregation for non-stream callers.
- `POST /v1/chat/completions`: **limited** compatibility layer for text, image URLs, function tools in a `namespace`, tool results, and streaming or aggregated results.
- Only `api.openai.com/v1/models`, `api.openai.com/v1/responses`, and `auth.openai.com` OAuth/OIDC endpoints are called.
- The admin dashboard binds only to `127.0.0.1:8787`; the separate Agent API uses port `8087` by default and supports access from private LAN hosts (with API key authentication). LAN HTTP is **unencrypted**. No telemetry.

## Requirements

Go 1.23 or later; macOS/Linux/Windows for development (macOS tested only for compilation in a Linux build environment; interactive sign-in needs your machine and browser). No third-party Go dependencies.

## One-click desktop dashboard (recommended)

On **macOS**, download/clone the project, then double-click **`start.command`** in Finder. The script builds the app (Go 1.23+ required) and starts the web console, opening your browser automatically. On first launch macOS may require **Right-click → Open** to allow a script downloaded from the internet.

Or start it manually on macOS, Linux or Windows:

```bash
go run . ui --dashboard-port 8787
```

The dashboard opens at `http://127.0.0.1:8787/ui/` using a one-time browser unlock flow. The **Agent API** uses `http://127.0.0.1:8087/v1` by default, **not** port 8787. The API and dashboard are different listeners.

Dashboard controls:

- Save a different **API port** and enable/disable **LAN access**. If the API is running, saving automatically rebinds it. When the requested port is unavailable, the old port is restored (or an explicit error is shown).
- On macOS, the dashboard offers a candidate `http://<Mac-LocalHostName>.local:8087/v1` address based on the machine's Bonjour LocalHostName. `.local` requires mDNS/Bonjour on the client; alternatively use the Mac's LAN IP.
- Manually choose a **16-256 character ASCII Agent API key**; old keys immediately stop working. This is separate from the ChatGPT OAuth access token and the dashboard unlock key.
- Start/stop/restart the **Agent API**. Restart the **entire program** or exit it. The dashboard is local-only even when the API is accessible on the LAN.

Keep the local service process running while using it. Closing Terminal or using "Quit Provider" stops both the web console and the API. A full restart reopens the browser with a fresh dashboard session.

If you accidentally close the browser tab, relaunch `./siwc-provider ui` **after stopping the existing process** to create a newly unlocked browser session. Alternatively, reopen the tab in the same browser while its session cookie is valid. Dashboard sessions expire after eight hours.

The dashboard's authentication uses a dedicated admin key in the owner-only local state file, separate from the key you give to Agents. The admin key travels in a localhost URL **fragment** that is removed from browser history immediately and is not sent to the HTTP server in the URL. The dashboard requires a local session cookie, strict Origin checks and an anti-CSRF header. Only reveal the Agent key when needed. The app has no online dashboard, no cloud token store and no telemetry.

**Important:** Browser OAuth and real plan eligibility have not been verified end-to-end; a visual login screen is not proof that SIWC authorization succeeds. SIWC terms and downstream usage restrictions still apply.

## Start (CLI alternative)

```bash
git clone https://github.com/yangjiechao/local-siwc-provider.git
cd local-siwc-provider
go test ./...
go build -o siwc-provider .

# Browser OAuth: Continue with ChatGPT
./siwc-provider login
./siwc-provider status

# Show your locally generated API key ONLY when needed; keep it secret
./siwc-provider key

# Start the local API
./siwc-provider serve --port 8087
```

State is stored at `~/.config/local-siwc-provider/state.json` with directory permissions `0700` and file permissions `0600`. Override the path with `SIWC_STATE_FILE=/path/to/state.json` if necessary. Do not put state files in a Git repository or share them. This initial release uses protected-file storage rather than macOS Keychain.

### Local client configuration

The local Bearer key returned by `./siwc-provider key` is **not** your OpenAI access token. Put only the local key in your existing agent configuration:

```yaml
base_url: http://127.0.0.1:8087/v1
api_key: <value from ./siwc-provider key>
model: <slug returned by GET /v1/models>
```

Example model discovery:

```bash
export LOCAL_SIWC_KEY="$(./siwc-provider key | tail -n 1)"
curl -s http://127.0.0.1:8087/v1/models \
  -H "Authorization: Bearer $LOCAL_SIWC_KEY"
```

Example **native Responses** request:

```bash
curl -N http://127.0.0.1:8087/v1/responses \
  -H "Authorization: Bearer $LOCAL_SIWC_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<available-model-slug>","input":[{"role":"user","content":"Hello!"}],"stream":true}'
```

The adapter always sends `store:false,stream:true` to the upstream SIWC route. It rejects known unsupported top-level fields and unsupported hosted tools rather than silently dropping them. It does not switch to API-key billing if the ChatGPT account is ineligible or rate limited.

## Security & limitations

- **Single locally selected registration, not an account pool.** No remote proxy, public endpoint, TLS fingerprint spoofing, ChatGPT internal API, or reuse of official CLI tokens.
- OAuth tokens remain local. The command `key` intentionally reveals *only* the randomly generated **local key**, upon user request. Do not screenshot or publish it.
- Redirects are disabled on HTTP clients; OIDC `jwks_uri` and `revocation_endpoint` must resolve to `auth.openai.com` over HTTPS.
- Returning sign-in reuses the issued `client_id`; the retained ID-token hint is not put in printed URLs. `logout` attempts OIDC refresh-token revocation and preserves the account registration mapping.
- Agent API routes require a Bearer key and reject requests containing an `Origin` header. API clients must originate on the host or private/local network and use localhost, private IP or the Mac's Bonjour hostname. Browser dashboard routes are **not mounted** on the Agent API listener. The dashboard uses a separate authenticated session and is always bound to loopback. Never forward the Agent API port on a router or expose it to public networks; API requests (including API keys and prompts) are **plaintext HTTP** on LAN, so use only a trusted local network. Cloud hosting/public exposure, browser-based cross-origin clients, or remote Internet proxies are not supported.
- Strict Chat Completions compatibility only. **Unsupported model parameters are rejected explicitly** (including `temperature`, `max_completion_tokens`, `response_format`, etc.). `max_output_tokens` is the exception: accepted and silently removed for SIWC (its limit does not apply). No fabricated JSON mode, false max-token guarantee, previous-response continuation, or hosted tools.
- The adapter translates text and local function tools; advanced multi-step reasoning items, audio/video, and full multimodal history are **not** preserved in Chat Completions mode. For complex reasoning, prefer native `/v1/responses`.
- SSE fails closed for aggregated calls unless `response.completed` arrives. Raw streaming follows upstream events and the caller must check the terminal status. Unexpected streaming failures cannot retroactively change an HTTP 200 status.
- Neither SIWC model eligibility nor tool namespaces have been live-tested against a real account. `tool_choice` and parallel tool behavior can vary by model and account.
- Run **one** instance per state file: cross-process refresh-token coordination is not implemented.
- Multi-agent relaying through a generic local provider is not automatically permitted just because this repository is open source; seek clarification if needed.

## Roadmap

- End-to-end interactive OAuth testing on macOS, then streaming and tool-turn tests with a real eligible account.
- Real-world Harness compatibility suite and proper handling of reasoning items.
- macOS Keychain support, account selection, and cross-process refresh locking.
- Optional safe packaging and signed releases.

## Official reference documents

- [SIWC Quickstart](https://developers.openai.com/siwc/quickstart)
- [Registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [Accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
- [Models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)
- [Preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)
- [Errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)

## License

MIT. This is a community project, not affiliated with or endorsed by OpenAI.


## Dashboard auto-update (rolling main-branch builds)

The **Check for updates** panel displays the running binary's embedded source commit and GitHub's current `main` commit. This repository currently tracks commit IDs rather than stable semver releases. Comparison uses GitHub's commit ancestry API and enables one-click updates only when `main` is ahead of the running build.

One-click updates require an official Git clone with a clean `main` branch, the exact locally built `siwc-provider` binary in the repository root, and installed Git and Go 1.23+. `start.command` embeds the Git SHA into builds, enabling reliable version comparisons. `go run`, custom/forked remotes, dirty or diverged branches, unknown binary revisions, standalone binaries, and Windows currently **do not** support automatic executable replacement.

On update, the app fetches from the pinned project GitHub origin, verifies fast-forward ancestry, creates a temporary Git worktree, runs `go test ./...` and `go build` in the worktree, then fast-forwards the main checkout, replaces the binary with a backup (`siwc-provider.previous`), and restarts the application. Failed tests/builds do not replace the current executable. Updating will interrupt active Agent requests. OAuth credentials and API settings in the protected state directory are not modified. The update endpoint is available only from the authenticated loopback dashboard, never from the LAN-facing Agent API.

For the first update to a version that contains this feature, run `git pull origin main` and launch `start.command` once to compile a binary with its source commit embedded. Future upgrades can be managed from the dashboard. You should still review source changes before installing any downloaded code, especially in security-sensitive installations.

## Multiple model providers (preview)

The local **Model Providers** dashboard lets the owner add independent API providers with ID, name, type, HTTPS base URL, separate API key, enabled flag and optional manually registered model IDs. The provider starts a background model-directory refresh on launch; you can refresh again from the dashboard. Discovery failures are visible, and manual model IDs are retained. External credentials are stored only in the local `0600` state file, never in GitHub.

The Agent endpoint remains `http://127.0.0.1:8087/v1` (or the configured LAN address). `GET /v1/models` returns the union of authorized SIWC models and external providers. External models use `provider-id/upstream-model-id`, e.g. `deepseek/deepseek-flash`, to avoid collisions. The original model ID is sent to the selected provider with its own API key. Unprefixed IDs first match the current user's SIWC catalog, then one unique external model. If SIWC discovery fails, the gateway does not guess and fall back to another paid source. Errors and rate limits are never an excuse for changing provider or billing. No API keys are included in list responses.

- **OpenAI-compatible providers**: `GET /models`, `POST /chat/completions`, and `POST /responses` (only if the selected provider itself implements it). No guarantee that every provider supports the Responses route or identical request parameters.
- **Ark Video / Seedance**: use `POST /v1/contents/generations/tasks` with a prefixed model ID, and `GET /v1/contents/generations/tasks/{task_id}?provider=<id>` to poll. Configure the Ark API v3 HTTPS base URL, manually register endpoint/model IDs when discovery is unavailable, and supply an independent Ark API key. Video responses are asynchronous and **not** Chat Completions.
- Provider endpoints must be public HTTPS domains. The adapter blocks redirects and private, local and link-local destinations to reduce accidental key leakage and SSRF.

**Example**: DeepSeek provider ID `deepseek`, type `openai-compatible`, URL `https://api.deepseek.com`, API key from your own DeepSeek account. Use the exact models returned by DeepSeek's `/models` endpoint. For Seedance, choose type `ark-video` and supply the official Ark video model/endpoint ID. Provider-specific subscriptions, access requirements, prices, compatibility and privacy controls must be verified directly with the vendor.

**Legal boundary:** multi-provider routing for independently authorized APIs is not itself SIWC subscription sharing; it does not authorize a generic ChatGPT-plan proxy for arbitrary external tools. Read [NOTICE-SIWC-COMPLIANCE.md](NOTICE-SIWC-COMPLIANCE.md) before integrating with any third-party app.

**SIWC parameter compatibility:** `max_output_tokens` is accepted on the local Responses and Chat Completions endpoints but intentionally removed before SIWC upstream calls. The requested output cap is **not enforced**. Independently authorized external providers receive their own request parameters unchanged (except the model prefix).
