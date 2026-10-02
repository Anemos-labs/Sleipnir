# Providers

| dialect | status | notes |
|---|---|---|
| OpenAI-style chat completions (OpenAI, Heimdall, OpenRouter, vLLM, SGLang, ...) | supported; measured against Heimdall (nine models, `docs/VALIDATION.md`) | automatic prefix caching, routing key for engine affinity, exact gateway costs, reasoning replay, optional token capture |
| Anthropic Messages (Anthropic, and gateways that speak it) | implemented: dialect `anthropic` (`docs/CONFIGURATION.md`) | explicit breakpoints (max 4, 20-block lookback, 5m/1h TTL), preserved thinking, turn-scoped hot tail; per-gateway switches for what a route drops (`cache_control`, thinking, mid-conversation system text); measure your endpoint with `sleipnir doctor --deep`; one live measurement, of Heimdall's `/messages` route |
| OpenAI Responses | not built yet (`openai-responses` is accepted in config, but a session that uses it stops with an error) | |

