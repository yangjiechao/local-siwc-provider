# Local SIWC Provider

**[简体中文：本地网页控制台使用指南](README.zh-CN.md)**

## Web dashboard (new in 0.2 preview)

No need to run separate login, status, serve, key and models commands. The localhost browser console now handles ChatGPT authorization, API start/stop, key reveal/copy, model discovery and agent configuration copying.

**macOS:** Install Go 1.23+ once, clone this repository, then **double-click `start.command`** in Finder. If Gatekeeper blocks a downloaded script, right-click and choose **Open**. The script builds the executable and opens the local browser console.

**Other platforms or terminal users:**

```bash
go run . ui --port 8787
```

The management dashboard is on `http://127.0.0.1:8787/ui/`, while compatible agents connect to `http://127.0.0.1:8787/v1`. Keep the local process running while using it. The dashboard has a separate administrator key and an authenticated, same-origin browser session; the agent's Bearer key is never used to unlock the administrator dashboard.

> This is still an experimental preview. Real SIWC OAuth, plan eligibility, and complex Harness tool-calling require live testing. Open source does not automatically authorize relaying subscription access to arbitrary closed-source agents.


An **experimental**, single-user, localhost-only Go adapter for [Sign in with ChatGPT (SIWC)](https://developers.openai.com/siwc/quickstart) and OpenAI's public **Responses API**. It enables eligible clients to authenticate with ChatGPT and supplies a limited OpenAI-compatible interface for local tools.

> **Preview / not yet live-verified.** The OAuth, function-namespace, SSE, and Chat Completions mapping implementation has local unit tests, but it has **not** been authenticated against a real ChatGPT account or tested with a production Harness. Do not interpret publication or a working OAuth callback as OpenAI approval for all downstream uses. In particular, whether a generic OSS adapter can relay subscription usage to independent private agents is a policy/authorization question that must be confirmed before deployment. No bypass or account pooling is included.

## Scope

- Official SIWC **dynamic open-source client registration** (`dynamic_agent_client` is used only for first registration).
- OAuth authorization code with PKCE S256, `state`, `nonce`, issued `client_id`, and **ID token RS256/JWKS verification**.
- Protected local state file (`0600`) with a stable per-host identifier, OAuth credentials, and a randomly generated **local-only** API key.
- Automatic serialized refresh of an expiring OAuth access token (one running process per state file).
- `GET /v1/models`: translates account-specific SIWC model list to an OpenAI-compatible `data[]` list.
- `POST /v1/responses`: forwards supported requests; always enforces upstream `store:false` and `stream:true`. Supports streaming downstream or aggregation for non-stream callers.
- `POST /v1/chat/completions`: **limited** compatibility layer for text, image URLs, function tools in a `namespace`, tool results, and streaming or aggregated results.
- Only `api.openai.com/v1/models`, `api.openai.com/v1/responses`, and `auth.openai.com` OAuth/OIDC endpoints are called.
- The HTTP server binds to `127.0.0.1` and authenticates requests with an independently generated local key. No telemetry.

## Requirements

Go 1.23 or later; macOS/Linux/Windows for development (macOS tested only for compilation in a Linux build environment; interactive sign-in needs your machine and browser). No third-party Go dependencies.

## Start

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
./siwc-provider serve --port 8787
```

State is stored at `~/.config/local-siwc-provider/state.json` with directory permissions `0700` and file permissions `0600`. Override the path with `SIWC_STATE_FILE=/path/to/state.json` if necessary. Do not put state files in a Git repository or share them. This initial release uses protected-file storage rather than macOS Keychain.

### Local client configuration

The local Bearer key returned by `./siwc-provider key` is **not** your OpenAI access token. Put only the local key in your existing agent configuration:

```yaml
base_url: http://127.0.0.1:8787/v1
api_key: <value from ./siwc-provider key>
model: <slug returned by GET /v1/models>
```

Example model discovery:

```bash
export LOCAL_SIWC_KEY="$(./siwc-provider key | tail -n 1)"
curl -s http://127.0.0.1:8787/v1/models \
  -H "Authorization: Bearer $LOCAL_SIWC_KEY"
```

Example **native Responses** request:

```bash
curl -N http://127.0.0.1:8787/v1/responses \
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
- Agent API routes require a Bearer key, reject requests containing an `Origin` header, and verify loopback Host. Browser dashboard routes use a separate authenticated session and anti-CSRF checks. Public browser integrations and multi-device use are intentionally unsupported.
- Strict Chat Completions compatibility only. **Unsupported model parameters are rejected explicitly** (including `temperature`, `max_completion_tokens`, `response_format`, etc.). No fabricated JSON mode, false max-token guarantee, previous-response continuation, or hosted tools.
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
