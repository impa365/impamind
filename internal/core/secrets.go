package core

import (
	"regexp"
	"sort"
)

type secretRule struct {
	name string
	re   *regexp.Regexp
}

var secretRules = []secretRule{
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"openai_key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`)},
	{"anthropic_key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"github_token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`)},
	{"gitlab_token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"slack_token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{"stripe_key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{"impamind_token", regexp.MustCompile(`\bimp[o]?_[A-Za-z0-9_-]{30,}`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"bearer_header", regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	{"url_credentials", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]{6,}@`)},
	{"password_assignment", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|senha|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b["']?\s*[:=]\s*["']?[^\s"',;]{8,}`)},
}

// ScanSecrets returns the names of secret patterns found in the texts. The
// matched values themselves are never returned or logged.
func ScanSecrets(texts ...string) []string {
	found := map[string]bool{}
	for _, t := range texts {
		if t == "" {
			continue
		}
		for _, r := range secretRules {
			if !found[r.name] && r.re.MatchString(t) {
				found[r.name] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MaskSecrets replaces every secret match with a placeholder.
func MaskSecrets(s string) string {
	for _, r := range secretRules {
		s = r.re.ReplaceAllString(s, "[REDACTED:"+r.name+"]")
	}
	return s
}
