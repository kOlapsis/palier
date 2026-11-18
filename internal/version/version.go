// Package version parses and orders image tags written as dotted versions.
package version

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// V is a tag read as a prefix such as "v" or "pg14-v", dotted numbers and a variant suffix such as "-alpine" or "-ce.0",
// with the PostgreSQL version the tag names, if any; a build number such as "stable-11248-2" is read as 0.11248.2.
type V struct {
	Tag    string
	Prefix string
	Nums   []int
	Suffix string
	Engine []int
}

var pattern = regexp.MustCompile(`^(?:(v?)(\d+(?:\.\d+)*)|([A-Za-z][\w.-]*?[-_]v?)(\d+(?:\.\d+)+))(.*)$`)

// Parse reads tag as a version; ok is false when it does not start with one.
func Parse(tag string) (v V, ok bool) {
	m := pattern.FindStringSubmatch(tag)
	if m == nil {
		return build(tag)
	}
	prefix, nums := m[1], m[2]
	if m[3] != "" {
		prefix, nums = m[3], m[4]
	}
	v = V{Tag: tag, Prefix: prefix, Suffix: m[5]}
	for _, n := range strings.Split(nums, ".") {
		i, err := strconv.Atoi(n)
		if err != nil {
			return V{}, false
		}
		v.Nums = append(v.Nums, i)
	}
	if e, ok := Engine(tag); ok {
		v.Engine = e.Nums
	}
	return v, true
}

// shortcut: only the "stable-" channel is read as a line of builds, add a channel when another project names its releases so.
var buildLine = regexp.MustCompile(`^(stable-)(\d+)(?:-(\d+))?$`)

// build reads a tag made of a release channel and a build number, as "stable-11248" or its revision "stable-11248-2".
func build(tag string) (V, bool) {
	m := buildLine.FindStringSubmatch(tag)
	if m == nil {
		return V{}, false
	}
	// a line of builds has no major: a build is read as a minor, its revision as a patch
	v := V{Tag: tag, Prefix: m[1], Nums: []int{0, 0, 0}}
	for i, n := range m[2:] {
		if n == "" {
			continue
		}
		var err error
		if v.Nums[i+1], err = strconv.Atoi(n); err != nil {
			return V{}, false
		}
	}
	return v, true
}

var engineNumber = regexp.MustCompile(`(pg|postgres(?:ql)?)\d+(?:\.\d+)*`)

var engineTag = regexp.MustCompile(`(?:^|[-_.])(?:pg|postgres(?:ql)?)(\d+(?:\.\d+)*)(?:[-_.]|$)`)

// Engine reads the PostgreSQL version a tag names beside its own, as 14 in "pg14-v0.2.0" or 16 in "2.17.2-pg16".
func Engine(tag string) (V, bool) {
	m := engineTag.FindStringSubmatch(tag)
	if m == nil {
		return V{}, false
	}
	return Parse(m[1])
}

// Extends reports whether w is a more precise tag of the line v names, as "2.2.7" extends "2.2".
func (v V) Extends(w V) bool {
	return v.Prefix == w.Prefix && v.Suffix == w.Suffix && len(w.Nums) > len(v.Nums) && slices.Equal(v.Nums, w.Nums[:len(v.Nums)])
}

// SameLine reports whether w is written like v: same prefix, same number of components, same suffix, the PostgreSQL
// version they name aside.
func (v V) SameLine(w V) bool {
	line := func(s string) string { return engineNumber.ReplaceAllString(s, "$1") }
	return line(v.Prefix) == line(w.Prefix) && len(v.Nums) == len(w.Nums) && line(v.Suffix) == line(w.Suffix)
}

// Less orders two versions of the same line, by the PostgreSQL version they name first.
func (v V) Less(w V) bool {
	if len(v.Engine) > 0 && len(w.Engine) > 0 && !slices.Equal(v.Engine, w.Engine) {
		return less(v.Engine, w.Engine)
	}
	return less(v.Nums, w.Nums)
}

// Major is the PostgreSQL major the tag names, else its first number.
func (v V) Major() int {
	if len(v.Engine) > 0 {
		return v.Engine[0]
	}
	return v.Nums[0]
}

func less(a, b []int) bool {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// Newer returns the tags of the same line as current and above it, in ascending order; a tag that names a newer
// PostgreSQL but an older version of its own is left out.
func Newer(current V, tags []string) []V {
	var out []V
	for _, t := range tags {
		w, ok := Parse(t)
		if ok && current.SameLine(w) && current.Less(w) && !less(w.Nums, current.Nums) {
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
