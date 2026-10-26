package secrets

import "testing"

func TestDetect(t *testing.T) {
	secret := map[string]string{
		"aws-access-key":    "AKIAIOSFODNN7EXAMPLE",
		"github-token":      "ghp_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		"ai-api-key":        "sk-ant-api03-" + "abcdefghijklmnopqrstuvwxyz012345",
		"openai-key":        "sk-proj-" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD",
		"stripe-key":        "sk_live_" + "51HxyzABCDEFGHIJKLMNOPQRS",
		"slack-token":       "xoxb-" + "1234567890-abcdefghij",
		"google-api-key":    "AIza" + "SyA1234567890abcdefghijklmnopqrstuv",
		"jwt":               "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"private-key":       "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
		"connection-string": "postgres://admin:hunter2pass@db.example.com:5432/app",
		"assignment":        "DB_PASSWORD=correct-horse-battery",
		"one-time-code":     "482913",
		"card-number":       "4242 4242 4242 4242",
		"password":          "Tr0ub4dor&3xK!",
	}
	for kind, text := range secret {
		f := Detect(text)
		if f == nil || f.Kind != kind {
			t.Errorf("Detect(%q) = %v, want kind %q", text, f, kind)
		}
	}

	safe := []string{
		"Hello from the other machine",
		"https://github.com/bkrdmrcioglu/clipferry",
		"/Users/bekir/Projects/clipferry/internal/core",
		"const userName = getUserName();",
		"getElementById",
		"snake_case_identifier",
		"2026",
		"README.md",
		"npm install --save-dev typescript",
		"4242 4242 4242 4241",
		"",
	}
	for _, text := range safe {
		if f := Detect(text); f != nil {
			t.Errorf("Detect(%q) = %v, want nil", text, f)
		}
	}
}
