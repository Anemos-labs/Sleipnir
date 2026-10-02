# Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported; measured against Heimdall (nine models, `docs/VALIDATION.md`) | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages (Anthropic, and gateways that speak it) | implemented: dialect `anthropic` (`docs/CONFIGURATION.md`) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail; per-gateway switches for what a route drops (`cache_control`, thinking, mid-conversation system text); measure your endpoint with `sleipnir doctor --deep`; one live measurement, of Heimdall's `/messages` route |
| OpenAI Responses (`openai-responses`: OpenAI's own newer route, and a ChatGPT plan) | implemented (`internal/provider/openairesp`): stateless (`store: false`), reasoning kept encrypted and sent back, OpenAI's automatic prefix cache with the cache key; tested against scripted streams and a fake endpoint, **not yet run on a live key or a live plan** | the plan's preview rules are followed as documented (tools in one namespace, no output limit or sampling parameters) |


## Subscription logins (a plan instead of an API key)

Checked on 2026-10-02 against the providers' own pages; terms change, so read them again before building anything on this.

* **Claude (Pro, Max): not supported, on purpose.** Anthropic's [legal and compliance page](https://code.claude.com/docs/en/legal-and-compliance) says OAuth
  authentication is "intended exclusively" for Claude's own apps, that developers building products or tools "should use API key authentication through Claude
  Console or a supported cloud provider", that third parties may not "route requests through Free, Pro, or Max plan credentials on behalf of their users", and
  may not "collect, store, or intermediate Claude.ai credentials or session tokens"; it may enforce this without notice (the press reported such blocks from January 2026). Use an API
  key (`sleipnir login anthropic`), or a cloud provider's Claude.
* **ChatGPT (Plus, Pro): allowed through OpenAI's own route, and built: `sleipnir login chatgpt`.** "Sign in with ChatGPT" has a token-sharing mode for open-source and
  locally hosted apps ([docs](https://developers.openai.com/siwc/token-sharing-open-source)): OAuth with PKCE and dynamic client registration, no client secret, scope
  `chatgpt.tokens.use.direct` against `https://api.openai.com/v1`, for "eligible Responses API requests" only, a weekly cap per app set by the user, and
  marked as a preview. `internal/chatgptauth` does the sign-in (the browser opens on OpenAI's page; where there is none, the address it ends on is pasted), keeps the
  tokens in `~/.sleipnir/chatgpt.json` (mode 0600; the tools of a session never see it, and the model's own reads of it ask), renews the access token before it
  expires (the refresh token rotates, so renewals are serialized and a renewal another process made is taken from the file), and `sleipnir logout chatgpt` revokes it.
  The provider is `chatgpt`: models are `chatgpt/<slug>` from the plan's own list (`sleipnir models --provider chatgpt`, the menus), they cost nothing per token (the
  plan's limits apply, and a limit that is reached is not retried), and the registration is this app's own, not the Codex CLI's. **It has been built from the documentation
  and tested against a fake issuer and a fake endpoint; it has not met a real account yet**, so the first real sign-in is a test: `sleipnir doctor --model chatgpt/<slug>
  --deep` says what the endpoint does, and an error from it is shown as it came. The preview's rules (`developers.openai.com/siwc/token-sharing-open-source/preview-limitations`)
  are followed as written: `store: false` and streaming, no `max_output_tokens`, `temperature` or the other refused parameters, and the function tools in one namespace.

## Built in

Name a model as `provider/model`. A key comes from its variable, else from `~/.sleipnir/auth.json` (`sleipnir login`); the variable wins. `sleipnir models` lists the
models of every provider that has a key, and of a local server that is running.

| provider | key variable | endpoint | verified |
|---|---|---|---|
| `heimdall` (recommended) | `HEIMDALL_API_KEY` | `api-staging.impossiblecarrot.cc/api/v1` | measured: nine models, the benchmark, daily use (`docs/VALIDATION.md`) |
| `openrouter` | `OPENROUTER_API_KEY` | `openrouter.ai/api/v1` | the adapter and the catalogue parser are tested against its published format; not run on a live key |
| `openai` | `OPENAI_API_KEY` | `api.openai.com/v1` | as above |
| `anthropic` | `ANTHROPIC_API_KEY` | `api.anthropic.com` (Messages dialect) | the adapter is tested against a mock of the Messages API; not run on a live key |
| `gemini` | `GEMINI_API_KEY` | `generativelanguage.googleapis.com/v1beta/openai` | built in; not run on a live key |
| `mistral` | `MISTRAL_API_KEY` | `api.mistral.ai/v1` | built in; not run on a live key |
| `xai` | `XAI_API_KEY` | `api.x.ai/v1` | built in; not run on a live key |
| `deepseek` | `DEEPSEEK_API_KEY` | `api.deepseek.com/v1` | built in; not run on a live key |
| `huggingface` | `HF_TOKEN` | `router.huggingface.co/v1` | built in; not run on a live key |
| `together`, `fireworks`, `groq`, `cerebras`, `deepinfra` | `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `DEEPINFRA_API_KEY` | their OpenAI-compatible routes | built in; not run on a live key |
| `sambanova`, `hyperbolic`, `nebius`, `novita`, `nvidia`, `parasail`, `baseten`, `chutes`, `siliconflow` | `SAMBANOVA_API_KEY`, `HYPERBOLIC_API_KEY`, `NEBIUS_API_KEY`, `NOVITA_API_KEY`, `NVIDIA_API_KEY`, `PARASAIL_API_KEY`, `BASETEN_API_KEY`, `CHUTES_API_KEY`, `SILICONFLOW_API_KEY` | `api.sambanova.ai/v1`, `api.hyperbolic.xyz/v1`, `api.tokenfactory.nebius.com/v1`, `api.novita.ai/openai/v1`, `integrate.api.nvidia.com/v1`, `api.parasail.io/v1`, `inference.baseten.co/v1`, `llm.chutes.ai/v1`, `api.siliconflow.com/v1` | built in; each base URL answered `GET /models` on 2026-10-02 (a catalogue, or the 401 that asks for a key); not run on a live key |
| `ollama-cloud`, `opencode` | `OLLAMA_API_KEY`, `OPENCODE_API_KEY` | `ollama.com/v1`, `opencode.ai/zen/v1` | as above |
| `moonshot`, `zai`, `minimax`, `dashscope`, `cohere` | `MOONSHOT_API_KEY`, `ZAI_API_KEY`, `MINIMAX_API_KEY`, `DASHSCOPE_API_KEY`, `COHERE_API_KEY` | the labs' own APIs of Kimi, GLM, MiniMax and Qwen (`api.moonshot.ai/v1`, `api.z.ai/api/paas/v4`, `api.minimax.io/v1`, the international `dashscope-intl.aliyuncs.com/compatible-mode/v1`) and Cohere's compatibility route (`api.cohere.ai/compatibility/v1`) | as above; a lab's China endpoint, or Z.ai's coding-plan one, is a `providers` entry with its own `base_url` |
| `ollama`, `lmstudio`, `llamacpp`, `vllm`, `sglang`, `jan` | none | `localhost:11434`, `:1234`, `:8080`, `:8000`, `:30000`, `:1337` (`/v1`) | found when running; the window is assumed to be 8192 tokens and the cost zero until you say otherwise (`options.context_window`); exercised against a mock server, not a live Ollama |

"Not run on a live key" means exactly that: the request and response shapes are the documented ones and `sleipnir doctor --model provider/model --deep` measures what a real endpoint
does (streaming, tools, cache reporting); run it before you rely on one, and report what it finds. Another endpoint that speaks chat completions or Messages is a `providers` entry
in your configuration (`docs/CONFIGURATION.md`).

A marketplace's model ids often start with the name of a vendor (`deepseek/deepseek-v4-flash`, `openai/gpt-oss-20b`, `anthropic/claude-...`): when no key is set for the provider of that
name and your default provider has one, the whole string is taken as the marketplace's model id.
