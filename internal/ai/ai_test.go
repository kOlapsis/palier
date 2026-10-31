package ai

import (
	"strings"
	"testing"
)

func TestSchemaIsStrict(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if props, ok := m["properties"].(map[string]any); ok {
			if m["additionalProperties"] != false {
				t.Errorf("%s: additionalProperties must be false", path)
			}
			if len(m["required"].([]string)) != len(props) {
				t.Errorf("%s: every property must be required", path)
			}
			for k, p := range props {
				walk(path+"."+k, p)
			}
		}
		walk(path+"[]", m["items"])
	}
	walk("plan", schema())
}

func TestReadStream(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"x\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"steps\\\":\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"[]}\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n"
	text, stop, err := readStream(strings.NewReader(stream))
	if err != nil || text != `{"steps":[]}` || stop != "end_turn" {
		t.Fatalf("%q %q %v", text, stop, err)
	}
}
