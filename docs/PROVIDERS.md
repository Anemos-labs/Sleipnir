# Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported; measured against Heimdall (nine models, `docs/VALIDATION.md`) | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages (Anthropic, and gateways that speak it) | implemented: dialect `anthropic` (`docs/CONFIGURATION.md`) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail; per-gateway switches for what a route drops (`cache_control`, thinking, mid-conversation system text); measure your endpoint with `sleipnir doctor --deep`; one live measurement, of Heimdall's `/messages` route |
| OpenAI Responses | not built yet (`openai-responses` is accepted in config, but a session that uses it stops with an error) | |


## Subscription logins (a plan instead of an API key)

Checked on 2026-10-02 against the providers' own pages; terms change, so read them again before building anything on this.

* **Claude (Pro, Max): not supported, on purpose.** Anthropic's [legal and compliance page](https://code.claude.com/docs/en/legal-and-compliance) says OAuth
  authentication is "intended exclusively" for Claude's own apps, that developers building products or tools "should use API key authentication through Claude
  Console or a supported cloud provider", that third parties may not "route requests through Free, Pro, or Max plan credentials on behalf of their users", and
  may not "collect, store, or intermediate Claude.ai credentials or session tokens"; it may enforce this without notice (the press reported such blocks from January 2026). Use an API
  key (`sleipnir login anthropic`), or a cloud provider's Claude.
* **ChatGPT (Plus, Pro): allowed through OpenAI's own route, not built yet.** "Sign in with ChatGPT" has a token-sharing mode for open-source and locally hosted
  apps ([docs](https://developers.openai.com/siwc/token-sharing-open-source)): OAuth with PKCE and dynamic client registration, no client secret, scope
  `chatgpt.tokens.use.direct` against `https://api.openai.com/v1`, for "eligible Responses API requests" only, a weekly cap per app set by the user, and
  marked as a preview. It needs the OpenAI Responses dialect first (the table above), then a `sleipnir login openai --plan` that runs the flow
  (`docs/ROADMAP.md`). Use the registration the documentation describes, not the Codex CLI's own client id.

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
| `ollama`, `lmstudio`, `llamacpp`, `vllm` | none | `localhost:11434`, `:1234`, `:8080`, `:8000` (`/v1`) | found when running; the window is assumed to be 8192 tokens and the cost zero until you say otherwise (`options.context_window`); exercised against a mock server, not a live Ollama |

"Not run on a live key" means exactly that: the request and response shapes are the documented ones and `sleipnir doctor --model provider/model --deep` measures what a real endpoint
does (streaming, tools, cache reporting); run it before you rely on one, and report what it finds. Another endpoint that speaks chat completions or Messages is a `providers` entry
in your configuration (`docs/CONFIGURATION.md`).

A marketplace's model ids often start with the name of a vendor (`deepseek/deepseek-v4-flash`, `openai/gpt-oss-20b`, `anthropic/claude-...`): when no key is set for the provider of that
name and your default provider has one, the whole string is taken as the marketplace's model id.
