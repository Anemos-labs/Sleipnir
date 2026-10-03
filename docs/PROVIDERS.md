# Providers

A model is selected as `provider/model`. Run `sleipnir models` to discover models
available through configured credentials and local servers.

## Authentication

`sleipnir login` configures a provider. API keys come from the provider's environment
variable or `~/.sleipnir/auth.json`; an environment variable takes precedence.
`sleipnir login chatgpt` uses OpenAI's browser sign-in and stores renewable tokens
in `~/.sleipnir/chatgpt.json`. `sleipnir logout chatgpt` revokes that login.

ChatGPT models use `chatgpt/<slug>` and the plan's usage limits. The integration
uses the [Sign in with ChatGPT token-sharing API](https://developers.openai.com/siwc/token-sharing-open-source).
Claude subscription login is not implemented; Anthropic models use an API key or
a configured gateway.

## API dialects

| Dialect | Request and cache behavior |
| --- | --- |
| `openai-chat` | Chat Completions; automatic prefix caching where supported by the server; gateway usage and reasoning replay |
| `openai-responses` | Stateless Responses with complete history and encrypted reasoning replay; persisted live-state notices preserve message boundaries |
| `anthropic` | Messages with explicit cache breakpoints; preserved thinking; optional turn-scoped system messages |

A compatible request format does not establish model capabilities, cache support,
cache retention, or a particular hit rate. Use `sleipnir doctor --model provider/model --deep`
to check the selected route. See [cache design](CACHE-DESIGN.md) and
[endpoint validation](VALIDATION.md).

## Built-in routes

| Provider | Credential |
| --- | --- |
| `chatgpt` | Browser sign-in with `sleipnir login chatgpt` |
| `openai` | `OPENAI_API_KEY` |
| `anthropic` | `ANTHROPIC_API_KEY` |
| `heimdall` | `HEIMDALL_API_KEY` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `gemini`, `mistral`, `xai`, `deepseek` | `GEMINI_API_KEY`, `MISTRAL_API_KEY`, `XAI_API_KEY`, `DEEPSEEK_API_KEY` |
| `huggingface` | `HF_TOKEN` |
| `together`, `fireworks`, `groq`, `cerebras`, `deepinfra` | `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`, `GROQ_API_KEY`, `CEREBRAS_API_KEY`, `DEEPINFRA_API_KEY` |
| `sambanova`, `hyperbolic`, `nebius`, `novita`, `nvidia`, `parasail`, `baseten`, `chutes`, `siliconflow` | `SAMBANOVA_API_KEY`, `HYPERBOLIC_API_KEY`, `NEBIUS_API_KEY`, `NOVITA_API_KEY`, `NVIDIA_API_KEY`, `PARASAIL_API_KEY`, `BASETEN_API_KEY`, `CHUTES_API_KEY`, `SILICONFLOW_API_KEY` |
| `moonshot`, `zai`, `minimax`, `dashscope`, `cohere` | `MOONSHOT_API_KEY`, `ZAI_API_KEY`, `MINIMAX_API_KEY`, `DASHSCOPE_API_KEY`, `COHERE_API_KEY` |
| `ollama-cloud`, `opencode` | `OLLAMA_API_KEY`, `OPENCODE_API_KEY` |
| `ollama`, `lmstudio`, `llamacpp`, `vllm`, `sglang`, `jan` | No key by default; local server must be running |

Local routes default to an 8192-token context window and zero monetary cost.
Set `options.context_window` to match the server. Server configuration determines
whether prefix caching is enabled and how requests share it.

Additional endpoints and dialect options are configured under `providers` in
[configuration](CONFIGURATION.md). A marketplace model ID can itself contain a
vendor prefix; use the marketplace provider explicitly to avoid ambiguity.
