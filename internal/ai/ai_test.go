package ai

import (
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
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

func TestReadChat(t *testing.T) {
	text, err := readChat([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"steps\":[]}"}}]}`))
	if err != nil || text != `{"steps":[]}` {
		t.Fatalf("%q %v", text, err)
	}
	for _, bad := range []string{`{"choices":[{"finish_reason":"length","message":{"content":"{"}}]}`, `{"choices":[]}`, `{"choices":[{"finish_reason":"stop","message":{"refusal":"no"}}]}`, `<html>`} {
		if _, err := readChat([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestNotesBlockWritesSharedNotesOnce(t *testing.T) {
	shared := notes.Note{Tag: "v3.4.1", URL: "https://example/v3.4.1", Body: "Server and machine learning v3.4.1"}
	st := &inventory.Stack{Services: []*inventory.Service{
		{Name: "immich-server", Notes: []notes.Note{shared}},
		{Name: "immich-machine-learning", Notes: []notes.Note{shared, {Tag: "v3.4.0", URL: "https://example/v3.4.0", Body: "only here"}}},
	}}
	block, _ := notesBlock(st)
	if strings.Count(block, shared.Body) != 1 || !strings.Contains(block, `services="immich-server, immich-machine-learning" version="v3.4.1"`) || !strings.Contains(block, `services="immich-machine-learning" version="v3.4.0"`) {
		t.Errorf("block:\n%s", block)
	}
}
