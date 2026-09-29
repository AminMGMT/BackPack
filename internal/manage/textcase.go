package manage

import "strings"

// titleWord is s with its first letter capitalised, for the menus' Title Case
// ("iran" → "Iran").
func titleWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
