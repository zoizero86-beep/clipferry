// Package locale reports the user's preferred language as a BCP 47 tag such
// as "tr-TR" or "en-US", or "" when it cannot be determined.
package locale

import "strings"

// Preferred returns the operating system's preferred UI language.
func Preferred() string { return normalize(preferred()) }

// Base returns the primary language subtag: "tr" for "tr-TR".
func Base(tag string) string {
	tag = strings.ToLower(normalize(tag))
	if i := strings.IndexByte(tag, '-'); i > 0 {
		return tag[:i]
	}
	return tag
}

func normalize(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, ".@"); i >= 0 { // "tr_TR.UTF-8"
		tag = tag[:i]
	}
	return strings.ReplaceAll(tag, "_", "-")
}
