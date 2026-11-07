// Package core holds pure domain rules: review policy, secret scanning,
// near-duplicate similarity, ranking and freshness.
package core

import (
	"time"

	"impamind/internal/store"
)

// InitialStatus decides whether a new agent write is active or proposed. Global
// writes are always proposed; within a project the scope policy decides.
// Nothing an agent sends (user_confirmed included) changes this.
func InitialStatus(scopeKind, policy, kind string) string {
	if store.Protected(scopeKind, policy, kind) {
		return "proposed"
	}
	return "active"
}

// ReviewAfter is the default review horizon per kind; zero means never.
func ReviewAfter(kind string) time.Duration {
	day := 24 * time.Hour
	switch kind {
	case "architecture", "business_rule", "requirement":
		return 180 * day
	case "decision", "pattern":
		return 180 * day
	case "preference":
		return 365 * day
	case "knowledge", "observation", "note":
		return 90 * day
	case "todo":
		return 30 * day
	}
	return 0
}

// DefaultTier is the tier a kind starts with when the caller does not set one.
func DefaultTier(kind string) int {
	switch kind {
	case "preference", "business_rule", "architecture", "requirement":
		return 1
	case "checkpoint", "session_summary", "observation":
		return 3
	}
	return 2
}

// SourceType derives the provenance from what the server can verify. Agents
// never get user_stated or human_ui.
func SourceType(human bool, ev []store.EvidenceIn) string {
	if human {
		return "human_ui"
	}
	for _, e := range ev {
		if store.ObservedEvidence(e.Type) && trimmed(e.Ref) != "" {
			return "tool_observed"
		}
	}
	return "agent_inferred"
}

func trimmed(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

// Freshness decays relevance by age; preferences decay slowly, sessions fast.
func Freshness(kind string, age time.Duration) float64 {
	half := 365 * 24 * time.Hour
	switch kind {
	case "preference", "business_rule", "architecture", "requirement":
		half = 3 * 365 * 24 * time.Hour
	case "decision", "pattern", "knowledge":
		half = 365 * 24 * time.Hour
	case "observation", "note", "todo":
		half = 120 * 24 * time.Hour
	case "checkpoint", "session_summary":
		half = 14 * 24 * time.Hour
	}
	if age <= 0 {
		return 1
	}
	x := float64(age) / float64(half)
	return 1 / (1 + x)
}

// StatusWeight keeps non-current memories out of the top unless asked for.
func StatusWeight(status string) float64 {
	switch status {
	case "active":
		return 1
	case "proposed":
		return 0.6
	case "deprecated":
		return 0.3
	case "superseded", "expired", "archived":
		return 0.15
	}
	return 0.05
}

func TierWeight(tier int) float64 {
	switch tier {
	case 1:
		return 1.25
	case 2:
		return 1
	}
	return 0.85
}

// Score combines relevance (higher is better) with the memory's metadata.
// Access count is only a small tie-breaker.
func Score(rel float64, m store.Memory, now time.Time) float64 {
	age := time.Duration(0)
	if t, ok := store.ParseTime(m.UpdatedAt); ok {
		age = now.Sub(t)
	}
	conf := 0.5 + 0.5*m.Confidence
	access := 1 + 0.02*minf(float64(m.AccessCount), 50)/50
	pin := 1.0
	if m.Pinned {
		pin = 1.15
	}
	return rel * StatusWeight(m.Status) * conf * Freshness(m.Kind, age) * TierWeight(m.Tier) * access * pin
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
