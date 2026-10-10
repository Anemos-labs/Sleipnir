/* ---------------------------------------------------------------------------------------------------------------
 * models, providers, role defaults. The columns of `sleipnir models` are REAL (cmd/sleipnir/models.go); the catalogue
 * entries are SAMPLE: the four anthropic ids are the only real ids and carry sample prices; every other id is an obvious
 * invention. inPerM = null means "price unknown" (a catalogue that does not say what it charges).
 * ------------------------------------------------------------------------------------------------------------- */
(function () {
  var fav = { 'anthropic/claude-sonnet-5-5': 1, 'anthropic/claude-haiku-5-5': 1, 'heimdall/demo-model': 1 };
  // provider, id, context, in, cached, out, tools, reasoning, [flags]
  var rows = [
    ['anthropic', 'claude-fable-5-1',  1000000, 15.00, 1.50, 75.00, true,  true],
    ['anthropic', 'claude-opus-5-5',    500000,  5.00, 0.50, 25.00, true,  true],
    ['anthropic', 'claude-sonnet-5-5',  500000,  3.00, 0.30, 15.00, true,  true],
    ['anthropic', 'claude-haiku-5-5',   200000,  1.00, 0.10,  5.00, true,  true],

    ['heimdall', 'demo-model',                      16000,  1.00, 0.10,  4.00, true,  false],
    ['heimdall', 'anthropic/claude-sonnet-5-5',    500000,  3.00, 0.30, 15.00, true,  true],
    ['heimdall', 'sample-flash-1',                 128000,  0.10, 0.01,  0.40, true,  false],
    ['heimdall', 'sample-flash-1-thinking',        128000,  0.15, 0.015, 0.60, true,  true],
    ['heimdall', 'sample-coder-32b',                64000,  0.30, 0.03,  1.20, true,  false],
    ['heimdall', 'sample-coder-480b',              256000,  1.20, 0.12,  4.80, true,  true],
    ['heimdall', 'sample-chat-8b',                  32000,  0.05, 0.005, 0.20, false, false],
    ['heimdall', 'sample-long-1m',                1000000,  0.60, 0.06,  2.40, true,  false],
    ['heimdall', 'sample-reasoner-70b',            128000,  0.90, 0.09,  3.60, true,  true],
    ['heimdall', 'sample-flash-2',                 256000,  0.12, 0.012, 0.48, true,  true],
    ['heimdall', 'sample-embed-small',               8000,  0.02, null,   0.00, false, false, { chat: false }],

    ['openrouter', 'sample/frugal-coder-32b',       64000,  0.20, 0.02,  0.80, true,  false],
    ['openrouter', 'sample/frugal-coder-32b:free',  32000,  0.00, 0.00,  0.00, true,  false],
    ['openrouter', 'sample/mid-reasoner-70b',      128000,  0.80, null,  3.20, true,  true],
    ['openrouter', 'sample/open-moe-235b',         256000,  0.40, 0.04,  1.60, true,  true],
    ['openrouter', 'sample/open-chat-7b',           32000,  null, null,  null, false, false],
    ['openrouter', 'sample/vision-flash',          128000,  0.15, 0.015, 0.60, true,  false],
    ['openrouter', 'sample/lab-large-2',          1000000,  2.00, 0.50,  8.00, true,  true],
    ['openrouter', 'sample/lab-small-2',           200000,  0.25, 0.025, 1.00, true,  false],
    ['openrouter', 'sample/legacy-completion',       8000,  null, null,  null, false, false],
    ['openrouter', 'sample/stealth-preview',       256000,  null, null,  null, true,  true],

    ['openai', 'sample-gpt-a',                     400000,  1.25, 0.125, 10.00, true,  true],
    ['openai', 'sample-gpt-a-mini',                400000,  0.25, 0.025,  2.00, true,  true],
    ['openai', 'sample-gpt-a-nano',                400000,  0.05, 0.005,  0.40, true,  false],
    ['openai', 'sample-gpt-b',                    1000000,  2.00, 0.50,   8.00, true,  false],
    ['openai', 'sample-gpt-b-mini',                1000000,  0.40, 0.10,   1.60, true,  false],
    ['openai', 'sample-gpt-moderation',              8000,  null, null,  null, false, false, { chat: false }],

    ['chatgpt', 'sample-plan-1',                   400000,  null, null,  null, true,  true,  { plan: true }],
    ['chatgpt', 'sample-plan-1-mini',              400000,  null, null,  null, true,  true,  { plan: true }],
    ['chatgpt', 'sample-plan-code',                272000,  null, null,  null, true,  true,  { plan: true }],

    ['local', 'qwen-sample-32b',                        0,  null, null,  null, true,  false],
    ['local', 'llama-sample-8b',                        0,  null, null,  null, false, false],
    ['local', 'deepseek-sample-r1-distill-14b',         0,  null, null,  null, true,  true],
  ];
  S.models = rows.map(function (r) {
    var o = r[8] || {}, plan = !!o.plan;
    var m = { ref: r[0] + '/' + r[1], provider: r[0], id: r[1], context: r[2], inPerM: r[3], cachedPerM: r[4], outPerM: r[5], tools: r[6], reasoning: r[7], favourite: !!fav[r[0] + '/' + r[1]],
      in: r[3], cached: r[4], out: r[5],   // short names of the price columns ($/M, null = price unknown)
      priceKnown: r[3] !== null || plan, plan: plan, chat: o.chat !== false, sample: true,
      pricing: plan ? 'plan' : (r[3] === null ? 'price unknown' : (r[3] === 0 && r[5] === 0 ? 'free' : 'sample price') ) };
    if (r[0] === 'local') { m.note = 'self-hosted vLLM at http://127.0.0.1:8000/v1: no price (zero cost), the catalogue does not say the context window (assumed 8192 until options.context_window says otherwise)'; m.priceKnown = false; m.pricing = 'price unknown'; }
    if (r[0] === 'anthropic') m.note = 'real model id, SAMPLE prices';
    return m;
  });
  S.modelsDefaults = { default: 'anthropic/claude-sonnet-5-5', favourites: Object.keys(fav) };
  /** modelsFilter(opts): the same filters as `sleipnir models` (words, tools, reasoning, maxPrice, minContext, fav, provider, all). */
  S.modelsFilter = function (o) {
    o = o || {};
    var words = (o.words || []).map(function (w) { return String(w).toLowerCase(); });
    var minc = o.minContext || 0;
    return S.models.filter(function (m) {
      if (!o.all && !m.chat) return false;
      if (o.provider && o.provider !== 'all' && m.provider !== o.provider) return false;
      if (o.tools && !m.tools) return false;
      if (o.reasoning && !m.reasoning) return false;
      if (o.fav && !m.favourite) return false;
      if (o.maxPrice > 0 && (m.plan || (m.outPerM === null ? 0 : m.outPerM) > o.maxPrice)) return false;   // REAL: an unknown price is 0 to the filter
      if (m.context < minc) return false;
      var ref = m.ref.toLowerCase();
      for (var i = 0; i < words.length; i++) if (ref.indexOf(words[i]) < 0) return false;
      return true;
    }).sort(function (a, b) { return a.favourite !== b.favourite ? (a.favourite ? -1 : 1) : a.ref < b.ref ? -1 : a.ref > b.ref ? 1 : 0; });
  };
  /** parseTokens("128k") = 128000, ("1m") = 1000000 (REAL: cmd/sleipnir parseTokens) */
  S.parseTokens = function (s) {
    var t = String(s).trim().toLowerCase(), mult = 1;
    if (/k$/.test(t)) { mult = 1e3; t = t.slice(0, -1); } else if (/m$/.test(t)) { mult = 1e6; t = t.slice(0, -1); }
    var v = parseFloat(t);
    if (!(v >= 0) || !/^[0-9.]+$/.test(t)) return NaN;
    return Math.floor(v * mult);
  };
  /** human(n): "?" 800 "128k" "1.0M" (REAL: cmd/sleipnir human) */
  S.humanTokens = function (n) { if (n <= 0) return '?'; if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'; if (n >= 1000) return Math.floor(n / 1000) + 'k'; return String(n); };
})();

S.providers = [
  { name: 'heimdall',  recommended: true, dialect: 'openai-chat', baseUrl: 'https://api-staging.impossiblecarrot.cc/api/v1', keyEnv: 'HEIMDALL_API_KEY', keyState: 'stored', keyWhere: '~/.sleipnir/auth.json (mode 0600)', envSet: false, signedIn: false,
    options: { session_header: true, cache_key_body: true }, headers: { 'X-Title': 'Sleipnir' }, usedBy: 'workers (heimdall/demo-model)', notes: 'a marketplace: its catalogue is public, `sleipnir models` lists it without a key; reports the cost of a call (usage.cost)' },
  { name: 'openrouter', dialect: 'openai-chat', baseUrl: 'https://openrouter.ai/api/v1', keyEnv: 'OPENROUTER_API_KEY', keyState: 'none', keyWhere: null, envSet: false, signedIn: false,
    options: { session_header: true }, headers: { 'X-Title': 'Sleipnir' }, usedBy: null, notes: 'no key: not asked by `sleipnir models` until `sleipnir login openrouter`' },
  { name: 'openai', dialect: 'openai-chat', baseUrl: 'https://api.openai.com/v1', keyEnv: 'OPENAI_API_KEY', keyState: 'env', keyWhere: 'environment variable OPENAI_API_KEY', envSet: true, signedIn: false,
    options: { cache_key_body: true }, headers: {}, usedBy: null, notes: 'the variable wins over anything stored; a ChatGPT plan is the chatgpt row, billed to the plan' },
  { name: 'anthropic', dialect: 'anthropic', baseUrl: 'https://api.anthropic.com', keyEnv: 'ANTHROPIC_API_KEY', keyState: 'env', keyWhere: 'environment variable ANTHROPIC_API_KEY', envSet: true, signedIn: false,
    options: {}, headers: {}, usedBy: 'manager (anthropic/claude-sonnet-5-5)', notes: 'Messages API with explicit cache breakpoints (shared_ttl 5m); Claude subscription login is not implemented' },
  { name: 'chatgpt', dialect: 'openai-responses', baseUrl: 'https://api.openai.com/v1', keyEnv: null, keyState: 'signed-in', keyWhere: '~/.sleipnir/chatgpt.json (mode 0600)', envSet: false, signedIn: true, who: 'ada@example.com (ChatGPT plan)',
    auth: 'chatgpt-plan', options: {}, headers: {}, usedBy: null, notes: '`sleipnir login chatgpt` signs in with the browser; the plan pays for the requests and its usage limits apply; prices read "plan"' },
  { name: 'local', dialect: 'openai-chat', baseUrl: 'http://127.0.0.1:8000/v1', keyEnv: null, keyState: 'not needed', keyWhere: null, envSet: false, signedIn: false, configured: true,
    options: { capture_tokens: true }, headers: {}, usedBy: null, notes: 'your own entry in ~/.sleipnir/config.json (providers.local): a self-hosted vLLM that records token ids for RL; no key; price unknown' },
];
S.providerNote = 'Built in beyond these six: anthropic, baseten, cerebras, chutes, cohere, dashscope, deepinfra, deepseek, fireworks, gemini, groq, huggingface, hyperbolic, minimax, mistral, moonshot, nebius, novita, nvidia, ollama-cloud, opencode, parasail, sambanova, siliconflow, together, xai, zai (REAL: sleipnir --help); local servers jan, llamacpp, lmstudio, ollama, sglang, vllm need no key.';
S.providerKeyVars = { heimdall: 'HEIMDALL_API_KEY', openrouter: 'OPENROUTER_API_KEY', openai: 'OPENAI_API_KEY', anthropic: 'ANTHROPIC_API_KEY', gemini: 'GEMINI_API_KEY', mistral: 'MISTRAL_API_KEY', xai: 'XAI_API_KEY', deepseek: 'DEEPSEEK_API_KEY', together: 'TOGETHER_API_KEY', fireworks: 'FIREWORKS_API_KEY', groq: 'GROQ_API_KEY', huggingface: 'HF_TOKEN' };

/* role -> model (the /roles table: REAL layout "  role  model  where it came from") */
S.roleModels = {
  // REAL layout of /roles: "  role  model  where it came from" (internal/session RoleModels): default, the manager, the other roles by name, the compactor
  rows: [
    { role: 'default',   model: 'anthropic/claude-sonnet-5-5', from: 'the session\'s model' },
    { role: 'manager',   model: 'anthropic/claude-sonnet-5-5', from: 'the session\'s model' },
    { role: 'backend',   model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'docs',      model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'frontend',  model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'fullstack', model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'reviewer',  model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'scout',     model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'tester',    model: 'heimdall/demo-model',         from: 'models.roles' },
    { role: 'compactor', model: '(each agent\'s own)',         from: 'no compactor model is set' },
  ],
  defaults: { manager: 'anthropic/claude-sonnet-5-5', worker: 'heimdall/demo-model', mailman: null, compactor: null },
  roles: ['manager', 'backend', 'frontend', 'fullstack', 'tester', 'reviewer', 'scout', 'docs', 'mailman', 'compactor'],
  note: 'A role without its own entry runs on the worker model; changing a role restarts the team on it, keeping the conversation of the manager and the board.',
};
/** roles: role -> model defaults (the /roles table). S.roleDefs has the role definitions (colours, short codes, pins). */
S.roles = S.roleModels;
S.efforts = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
