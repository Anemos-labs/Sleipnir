package mcptest

import "encoding/json"

// jsonUnmarshal decodes a mock-server JSON value using the standard decoder's rules.
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
