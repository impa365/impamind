// Package config reads the server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr      string
	DataDir   string
	DBPath    string
	PublicURL string

	AdminPassword  string
	BootstrapToken string
	Secret         string
	CookieSecure   bool
	CORSOrigins    []string

	MCPVersions []string

	ObservationBudget int
	ProposalTTL       time.Duration
	SessionArchive    time.Duration
	LogRetention      time.Duration

	BackupDir      string
	BackupInterval time.Duration
	BackupKeep     int

	RAG         bool
	EmbedURL    string
	EmbedKey    string
	EmbedModel  string
	EmbedDims   int
	EmbedBatch  int
	EmbedTimeout time.Duration

	JobsInterval time.Duration
	LogLevel     string
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(k string, def int) (int, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}

func envBool(k string, def bool) (bool, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", k, err)
	}
	return b, nil
}

func envDur(k string, def time.Duration) (time.Duration, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", k, err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return d, nil
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Load reads the configuration. Secrets may also come from *_FILE variables,
// which is how Docker secrets are mounted.
func Load() (Config, error) {
	var c Config
	var err error
	c.Addr = env("IMPAMIND_ADDR", ":30303")
	c.DataDir = env("IMPAMIND_DATA_DIR", "/data")
	c.DBPath = env("IMPAMIND_DB_PATH", filepath.Join(c.DataDir, "impamind.db"))
	c.PublicURL = strings.TrimRight(env("IMPAMIND_PUBLIC_URL", ""), "/")
	if c.AdminPassword, err = secret("IMPAMIND_ADMIN_PASSWORD"); err != nil {
		return c, err
	}
	if c.BootstrapToken, err = secret("IMPAMIND_BOOTSTRAP_TOKEN"); err != nil {
		return c, err
	}
	if c.Secret, err = secret("IMPAMIND_SECRET"); err != nil {
		return c, err
	}
	if c.CookieSecure, err = envBool("IMPAMIND_COOKIE_SECURE", strings.HasPrefix(c.PublicURL, "https://")); err != nil {
		return c, err
	}
	c.CORSOrigins = list(env("IMPAMIND_CORS_ORIGINS", ""))
	c.MCPVersions = list(env("IMPAMIND_MCP_VERSIONS", "2026-07-28,2025-11-25"))
	if c.ObservationBudget, err = envInt("IMPAMIND_SESSION_OBSERVATION_BUDGET", 15); err != nil {
		return c, err
	}
	if c.ProposalTTL, err = envDur("IMPAMIND_PROPOSAL_TTL", 30*24*time.Hour); err != nil {
		return c, err
	}
	if c.SessionArchive, err = envDur("IMPAMIND_SESSION_ARCHIVE_AFTER", 14*24*time.Hour); err != nil {
		return c, err
	}
	if c.LogRetention, err = envDur("IMPAMIND_LOG_RETENTION", 90*24*time.Hour); err != nil {
		return c, err
	}
	c.BackupDir = env("IMPAMIND_BACKUP_DIR", filepath.Join(c.DataDir, "backups"))
	if c.BackupInterval, err = envDur("IMPAMIND_BACKUP_INTERVAL", 24*time.Hour); err != nil {
		return c, err
	}
	if c.BackupKeep, err = envInt("IMPAMIND_BACKUP_KEEP", 7); err != nil {
		return c, err
	}
	if c.RAG, err = envBool("IMPAMIND_RAG", false); err != nil {
		return c, err
	}
	c.EmbedURL = strings.TrimRight(env("IMPAMIND_EMBED_URL", env("OPENAI_BASE_URL", "https://api.openai.com/v1")), "/")
	if c.EmbedKey, err = secret("IMPAMIND_EMBED_KEY"); err != nil {
		return c, err
	}
	if c.EmbedKey == "" {
		if c.EmbedKey, err = secret("OPENAI_API_KEY"); err != nil {
			return c, err
		}
	}
	c.EmbedModel = env("IMPAMIND_EMBED_MODEL", "text-embedding-3-small")
	if c.EmbedDims, err = envInt("IMPAMIND_EMBED_DIMENSIONS", 512); err != nil {
		return c, err
	}
	if c.EmbedBatch, err = envInt("IMPAMIND_EMBED_BATCH", 32); err != nil {
		return c, err
	}
	if c.EmbedTimeout, err = envDur("IMPAMIND_EMBED_TIMEOUT", 20*time.Second); err != nil {
		return c, err
	}
	if c.JobsInterval, err = envDur("IMPAMIND_JOBS_INTERVAL", 10*time.Minute); err != nil {
		return c, err
	}
	c.LogLevel = env("IMPAMIND_LOG_LEVEL", "info")
	return c, c.Validate()
}

func secret(k string) (string, error) {
	if f := env(k+"_FILE", ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", k, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return env(k, ""), nil
}

func (c Config) Validate() error {
	if c.ObservationBudget < 1 {
		return fmt.Errorf("IMPAMIND_SESSION_OBSERVATION_BUDGET must be >= 1")
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 10 {
		return fmt.Errorf("IMPAMIND_ADMIN_PASSWORD must have at least 10 characters")
	}
	if c.BootstrapToken != "" && len(c.BootstrapToken) < 24 {
		return fmt.Errorf("IMPAMIND_BOOTSTRAP_TOKEN must have at least 24 characters")
	}
	if c.RAG && c.EmbedKey == "" && !strings.HasPrefix(c.EmbedURL, "http://") {
		return fmt.Errorf("IMPAMIND_RAG=true needs IMPAMIND_EMBED_KEY or OPENAI_API_KEY")
	}
	if c.EmbedDims < 0 || c.EmbedDims > 4096 {
		return fmt.Errorf("IMPAMIND_EMBED_DIMENSIONS out of range")
	}
	if len(c.MCPVersions) == 0 {
		return fmt.Errorf("IMPAMIND_MCP_VERSIONS must list at least one version")
	}
	return nil
}
