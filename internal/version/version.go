// Package version parses and orders image tags written as dotted versions.
package version

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// V is a tag read as an optional "v", dotted numbers and a variant suffix such as "-alpine" or "-ce.0".
type V struct {
	Tag    string
	Prefix string
	Nums   []int
	Suffix string
}

var pattern = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

// Parse reads tag as a version; ok is false when it does not start with one.
func Parse(tag string) (v V, ok bool) {
	m := pattern.FindStringSubmatch(tag)
	if m == nil {
		return V{}, false
	}
	v = V{Tag: tag, Prefix: m[1], Suffix: m[3]}
	for _, n := range strings.Split(m[2], ".") {
		i, err := strconv.Atoi(n)
		if err != nil {
			return V{}, false
		}
		v.Nums = append(v.Nums, i)
	}
	return v, true
}

// Precise reports whether v names a release rather than a moving line such as "16" or "latest".
func (v V) Precise() bool { return len(v.Nums) >= 2 }

// SameLine reports whether w is written like v: same prefix, same number of components, same suffix.
func (v V) SameLine(w V) bool {
	return v.Prefix == w.Prefix && len(v.Nums) == len(w.Nums) && v.Suffix == w.Suffix
}

// Less orders two versions of the same line.
func (v V) Less(w V) bool {
	for i := range min(len(v.Nums), len(w.Nums)) {
		if v.Nums[i] != w.Nums[i] {
			return v.Nums[i] < w.Nums[i]
		}
	}
	return len(v.Nums) < len(w.Nums)
}

// Newer returns the tags of the same line as current and above it, in ascending order.
func Newer(current V, tags []string) []V {
	var out []V
	for _, t := range tags {
		w, ok := Parse(t)
		if ok && current.SameLine(w) && current.Less(w) {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b V) int {
		switch {
		case a.Less(b):
			return -1
		case b.Less(a):
			return 1
		}
		return 0
	})
	return out
}
