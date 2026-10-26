// Package secrets decides whether a piece of copied text looks like a
// credential. Detection runs on the device, before anything leaves it.
package secrets

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Finding describes why a clip was considered sensitive.
type Finding struct {
	Kind  string `json:"kind"`  // machine-readable, e.g. "github-token"
	Label string `json:"label"` // human-readable, e.g. "GitHub token"
}

type rule struct {
	kind, label string
	re          *regexp.Regexp
}

// Ordered from most to least specific: the first match names the finding.
var rules = []rule{
	{"private-key", "Private key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{"aws-access-key", "AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b`)},
	{"github-token", "GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b`)},
	{"gitlab-token", "GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"ai-api-key", "AI provider API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"openai-key", "OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_\-]{32,}`)},
	{"stripe-key", "Stripe key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{20,}\b`)},
	{"slack-token", "Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`)},
	{"google-api-key", "Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"npm-token", "npm token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"jwt", "JSON Web Token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
	{"connection-string", "Connection string with password", regexp.MustCompile(`\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s@/]{3,}@[^\s]+`)},
	{"assignment", "Password or secret assignment", regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|api[_\-]?key|access[_\-]?token|auth[_\-]?token|client[_\-]?secret)["']?\s*[:=]\s*["']?[^\s"']{6,}`)},
}

var (
	otpRe  = regexp.MustCompile(`^\d{6,8}$`)
	cardRe = regexp.MustCompile(`^(?:\d[ \-]?){13,19}$`)
)

// Detect returns a finding if text looks like a secret, or nil.
func Detect(text string) *Finding {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	for _, r := range rules {
		if r.re.MatchString(t) {
			return &Finding{Kind: r.kind, Label: r.label}
		}
	}
	if otpRe.MatchString(t) {
		return &Finding{Kind: "one-time-code", Label: "One-time code"}
	}
	if cardRe.MatchString(t) && luhn(t) {
		return &Finding{Kind: "card-number", Label: "Payment card number"}
	}
	if looksLikePassword(t) {
		return &Finding{Kind: "password", Label: "Password-like string"}
	}
	return nil
}

// looksLikePassword flags a single short-to-medium token that mixes
// character classes and has high entropy — what a generated password or an
// unknown API key looks like — while leaving words, URLs, paths and code
// identifiers alone.
func looksLikePassword(t string) bool {
	if len(t) < 10 || len(t) > 100 || strings.ContainsAny(t, " \t\n") {
		return false
	}
	if strings.Contains(t, "://") || strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") || strings.Contains(t, "\\") {
		return false
	}
	var lower, upper, digit, symbol int
	for _, r := range t {
		switch {
		case unicode.IsLower(r):
			lower++
		case unicode.IsUpper(r):
			upper++
		case unicode.IsDigit(r):
			digit++
		default:
			symbol++
		}
	}
	classes := 0
	for _, n := range []int{lower, upper, digit, symbol} {
		if n > 0 {
			classes++
		}
	}
	// Plain identifiers (camelCase, snake_case, kebab-case) rarely contain digits
	// in the middle *and* symbols; require real mixing.
	if classes < 3 || digit == 0 {
		return false
	}
	return entropy(t) >= 3.3
}

// entropy is the Shannon entropy of s in bits per character.
func entropy(s string) float64 {
	freq := map[rune]float64{}
	n := 0.0
	for _, r := range s {
		freq[r]++
		n++
	}
	h := 0.0
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

func luhn(s string) bool {
	sum, alt, digits := 0, false, 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
		digits++
	}
	return digits >= 13 && sum%10 == 0
}

// LabelFor returns the human-readable label of a finding kind.
func LabelFor(kind string) string {
	for _, r := range rules {
		if r.kind == kind {
			return r.label
		}
	}
	switch kind {
	case "one-time-code":
		return "One-time code"
	case "card-number":
		return "Payment card number"
	case "password":
		return "Password-like string"
	case "password-manager":
		return "Password from a password manager"
	}
	return "Secret"
}
