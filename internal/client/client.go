// Package client is the local half of impamind: hooks and markdown sync.
// It talks to the server over HTTP and does not open a database.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Env configures the client from the process environment.
type Env struct {
	URL     string
	Token   string
	Project string
}

func FromEnv() Env {
	return Env{URL: strings.TrimRight(os.Getenv("IMPAMIND_URL"), "/"), Token: os.Getenv("IMPAMIND_TOKEN"), Project: os.Getenv("IMPAMIND_PROJECT")}
}

func (e Env) valid() error {
	if e.URL == "" || e.Token == "" {
		return fmt.Errorf("IMPAMIND_URL and IMPAMIND_TOKEN are required")
	}
	return nil
}

func (e Env) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.Project != "" {
		req.Header.Set("X-Impamind-Project", e.Project)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, bytes.TrimSpace(b))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Hook reads a client hook payload and writes the instruction the host should inject.
func Hook(args []string, in io.Reader, out io.Writer, env Env) error {
	if err := env.valid(); err != nil {
		return err
	}
	var payload map[string]any
	if err := json.NewDecoder(in).Decode(&payload); err != nil && err != io.EOF {
		return err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	if event == "" {
		event, _ = payload["hook_event_name"].(string)
	}
	switch strings.ToLower(event) {
	case "sessionstart", "session_start", "claude-session-start", "cursor-session-start":
		return recall(env, payload, out)
	case "precompact", "stop", "cursor-stop":
		fmt.Fprintln(out, "Se esta sessão alterou arquivos e ainda não há checkpoint, chame memory_checkpoint ou memory_handoff uma única vez.")
		return nil
	default:
		return nil
	}
}

func recall(env Env, payload map[string]any, out io.Writer) error {
	task, _ := payload["task"].(string)
	if task == "" {
		task = "retomar o trabalho neste repositório"
	}
	hint, _ := payload["cwd"].(string)
	var pack map[string]any
	err := env.do(http.MethodPost, "/api/recall", map[string]any{"task": task, "project_hint": hint, "project": env.Project}, &pack)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(pack)
	text := "Memória persistente do impamind para esta sessão:\n" + string(raw)
	_ = json.NewEncoder(out).Encode(map[string]any{"additionalContext": text, "hookSpecificOutput": map[string]any{"additionalContext": text}})
	return nil
}

// Sync pulls or pushes the markdown mirror of the current project.
func Sync(args []string, env Env) error {
	if err := env.valid(); err != nil {
		return err
	}
	if env.Project == "" {
		return fmt.Errorf("IMPAMIND_PROJECT is required")
	}
	dir := ".impamind"
	cmd := "pull"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "pull", "push":
			cmd = args[i]
		case "--dir":
			i++
			if i < len(args) {
				dir = args[i]
			}
		}
	}
	switch cmd {
	case "pull":
		return pull(env, dir)
	default:
		return push(env, dir)
	}
}

type exported struct {
	Memories []mem `json:"memories"`
}

type mem struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tier    int      `json:"tier"`
	Status  string   `json:"status"`
	Tags    []string `json:"tags"`
}

func pull(env Env, dir string) error {
	var out exported
	if err := env.do(http.MethodGet, "/api/sync/export?project="+urlQuery(env.Project), nil, &out); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range out.Memories {
		name := filepath.Join(dir, m.Kind, slug(m.Title)+".md")
		seen[name] = true
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(name, []byte(render(m)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func push(env Env, dir string) error {
	var items []map[string]any
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		m := parse(string(b))
		m["file"] = filepath.ToSlash(path)
		items = append(items, m)
		return nil
	})
	var res map[string]any
	return env.do(http.MethodPost, "/api/sync/import", map[string]any{"project": env.Project, "items": items}, &res)
}

func render(m mem) string {
	tags := strings.Join(m.Tags, ", ")
	return fmt.Sprintf("---\nid: %s\nkind: %s\ntitle: %s\nstatus: %s\ntier: %d\ntags: [%s]\n---\n%s\n", m.ID, m.Kind, yaml(m.Title), m.Status, m.Tier, tags, m.Content)
}

func parse(s string) map[string]any {
	out := map[string]any{"kind": "note", "hash": fmt.Sprintf("%x", len(s))}
	if !strings.HasPrefix(s, "---\n") {
		out["title"] = "nota"
		out["content"] = s
		return out
	}
	rest := s[4:]
	i := strings.Index(rest, "\n---\n")
	if i < 0 {
		out["content"] = s
		return out
	}
	for _, line := range strings.Split(rest[:i], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "[]")
		switch strings.TrimSpace(k) {
		case "id":
			out["id"] = v
		case "kind":
			out["kind"] = v
		case "title":
			out["title"] = v
		case "tier":
			fmt.Sscanf(v, "%d", new(int))
			var n int
			fmt.Sscanf(v, "%d", &n)
			out["tier"] = n
		case "tags":
			var tags []string
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			out["tags"] = tags
		}
	}
	out["content"] = strings.TrimSpace(rest[i+5:])
	return out
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
			b.WriteByte('-')
		}
		if b.Len() == 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = fmt.Sprintf("m%d", time.Now().UnixNano())
	}
	return out
}

func yaml(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func urlQuery(s string) string { return strings.ReplaceAll(s, " ", "%20") }
