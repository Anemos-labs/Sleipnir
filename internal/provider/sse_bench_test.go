package provider

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// A streamed answer is one SSE event per few tokens, and every event of every stream passes through the reader: a model that writes
// 2,000 tokens a turn is some 500 of these, fifty agents at a time.
func benchStream(chunks int) []byte {
	var b bytes.Buffer
	b.WriteString(": PROCESSING\n\n")
	for i := 0; i < chunks; i++ {
		fmt.Fprintf(&b, "data: {\"id\":\"gen-1790806348\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek/deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"},\"finish_reason\":null}]}\n\n", strings.Repeat("token ", 4))
	}
	b.WriteString("data: {\"id\":\"gen-1790806348\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":21000,\"completion_tokens\":2000}}\n\ndata: [DONE]\n\n")
	return b.Bytes()
}

func BenchmarkSSEReader(b *testing.B) {
	for _, chunks := range []int{50, 500} {
		stream := benchStream(chunks)
		b.Run(fmt.Sprintf("%d-chunks", chunks), func(b *testing.B) {
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := NewSSEReader(bytes.NewReader(stream))
				n := 0
				for {
					ev, err := r.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
					if len(ev.Data) > 0 {
						n++
					}
				}
				if n < chunks {
					b.Fatalf("%d events of %d", n, chunks)
				}
			}
		})
	}
}
