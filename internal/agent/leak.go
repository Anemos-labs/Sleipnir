package agent

import "regexp"

// A tool call written as text.
//
// A model behind a gateway that does not parse its chat format writes its call as plain text where the call should be: gpt-oss-20b, through
// Heimdall, ended a run with "to=functions.read <|constrain|>json<|message|>{...}<|call|>" as its answer. No tool ran, the work was not done,
// and the run took the text for the end of it (exit 0). A message that holds the markup of a chat format is not an answer. The model is
// told so, with the markup it wrote, and asked to make the call again or to answer in words; twice at most in a run, then its text is the
// answer, as it always was (a model that cannot stop is not asked for ever, and the answer may be about the markup).

// maxLeakNudges is how many times one Run sends a message of chat-format markup back.
const maxLeakNudges = 2

// leakMarkup finds the pieces of a chat format that only a call (or the end of a message) should hold:
//   - <|channel|>, <|call|>, <|constrain|>: gpt-oss "harmony" (the tokens are kept when a gateway forgets to cut them out);
//   - to=functions.NAME: harmony's address of a call, which is what is left when a gateway drops the special tokens;
//   - <tool_call>, </tool_call>, <function=NAME>: the Hermes and Qwen forms, as vLLM returns them without a tool parser;
//   - <｜tool▁call▁begin｜>: DeepSeek's (the bars are full-width).
var leakMarkup = regexp.MustCompile(`<\|[A-Za-z_]{2,24}\|>|to=functions\.[A-Za-z_][A-Za-z0-9_]*|</?tool_call>|<function=[A-Za-z_][A-Za-z0-9_]*>|<｜[^｜\n]{2,40}｜>`)

// leakedCall is the first piece of chat-format markup in an answer, or "" when there is none.
func leakedCall(text string) string {
	return leakMarkup.FindString(text)
}
