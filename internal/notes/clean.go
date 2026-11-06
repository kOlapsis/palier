package notes

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	mdComment   = regexp.MustCompile(`(?m)^[ \t]*\[[^\]\n]*\]:[ \t]*(?:#|<>)[ \t]*(?:\([^\n]*\)|"[^\n]*"|'[^\n]*')[ \t]*$`)
	hiddenTag   = regexp.MustCompile(`(?is)<(?:span|div|p|section|font|details|small)\b[^>]*\b(?:hidden|display\s*:\s*none|visibility\s*:\s*hidden|font-size\s*:\s*0)[^>]*>.*?</(?:span|div|p|section|font|details|small)\s*>`)
)

func invisible(r rune) bool {
	return r == 0xAD || r >= 0x200B && r <= 0x200F || r >= 0x202A && r <= 0x202E || r >= 0x2060 && r <= 0x2064 ||
		r >= 0x2066 && r <= 0x2069 || r == 0xFEFF || r >= 0xE0000 && r <= 0xE007F
}

// Clean removes from a note what a reader does not see but a model reads: comments, hidden elements and invisible characters, and returns what it removed.
func Clean(body string) (string, []string) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	var removed []string
	for _, re := range []*regexp.Regexp{htmlComment, mdComment, hiddenTag} {
		body = re.ReplaceAllStringFunc(body, func(m string) string {
			if t := strings.TrimSpace(m); t != "" {
				if r := []rune(t); len(r) > 300 {
					t = string(r[:300]) + "…"
				}
				removed = append(removed, t)
			}
			return ""
		})
	}
	count := 0
	body = strings.Map(func(r rune) rune {
		if invisible(r) {
			count++
			return -1
		}
		return r
	}, body)
	switch {
	case count == 1:
		removed = append(removed, "1 invisible character")
	case count > 1:
		removed = append(removed, fmt.Sprintf("%d invisible characters", count))
	}
	return body, removed
}
