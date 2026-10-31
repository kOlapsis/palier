// Package catalog holds the cards Palier ships: documentation excerpts, required upgrade stops and check commands of known images.
package catalog

import (
	"embed"
	"encoding/json"
	"slices"
)

//go:embed cards/*.json
var files embed.FS

// Card describes one known application.
type Card struct {
	ID       string              `json:"id"`
	Images   []string            `json:"images"`
	Excerpts []Excerpt           `json:"excerpts"`
	Stops    []string            `json:"stops,omitempty"`
	Checks   map[string][]string `json:"checks,omitempty"`
}

// Excerpt is a passage of the documentation, copied word for word, that facts may quote.
type Excerpt struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

var cards = func() []Card {
	entries, err := files.ReadDir("cards")
	if err != nil {
		panic(err)
	}
	var out []Card
	for _, e := range entries {
		data, err := files.ReadFile("cards/" + e.Name())
		if err != nil {
			panic(err)
		}
		var c Card
		if err := json.Unmarshal(data, &c); err != nil {
			panic(e.Name() + ": " + err.Error())
		}
		out = append(out, c)
	}
	return out
}()

// For returns the card of an image name such as docker.io/outlinewiki/outline, or nil.
func For(image string) *Card {
	for i := range cards {
		if slices.Contains(cards[i].Images, image) {
			return &cards[i]
		}
	}
	return nil
}
