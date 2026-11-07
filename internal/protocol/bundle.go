package protocol

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"impamind/internal/auth"
	"impamind/internal/store"
)

const bundleFormat = "impamind-bundle"
const bundleFormatVersion = "1"

// BundleExportOpts controls what goes into a .impamind file.
type BundleExportOpts struct {
	Passphrase      string
	IncludeGlobal   bool
	IncludeSessions bool   // v1: ignored (always false)
	Project         string // empty = all readable projects
}

// BundleImportOpts controls merge behaviour.
type BundleImportOpts struct {
	Passphrase     string
	OverwriteVault bool
	SkipLinks      bool // large graphs can exceed reverse-proxy timeouts
}

type bundleManifest struct {
	Format          string             `json:"format"`
	FormatVersion   string             `json:"format_version"`
	SchemaVersion   string             `json:"schema_version"`
	ExportedAt      string             `json:"exported_at"`
	IncludeGlobal   bool               `json:"include_global"`
	IncludeSessions bool               `json:"include_sessions"`
	Scopes          []bundleScopeBrief `json:"scopes"`
	Counts          bundleCounts       `json:"counts"`
	ContentHash     string             `json:"content_hash"`
}

type bundleScopeBrief struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type bundleCounts struct {
	Projects     int `json:"projects"`
	Memories     int `json:"memories"`
	Links        int `json:"links"`
	VaultEntries int `json:"vault_entries"`
	Aliases      int `json:"aliases"`
}

type bundlePayload struct {
	Projects []bundleProject `json:"projects"`
	Aliases  []bundleAlias   `json:"aliases"`
	Memories []bundleMemory  `json:"memories"`
	Links    []bundleLink    `json:"links"`
}

type bundleProject struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Hidden       bool     `json:"hidden,omitempty"`
	ReviewPolicy string   `json:"review_policy"`
	Tags         []string `json:"tags,omitempty"`
}

type bundleAlias struct {
	ProjectSlug string `json:"project_slug"`
	Kind        string `json:"kind"`
	Value       string `json:"value"`
}

type bundleMemory struct {
	ID           string               `json:"id"`
	ScopeSlug    string               `json:"scope_slug"`
	Kind         string               `json:"kind"`
	Title        string               `json:"title"`
	Content      string               `json:"content"`
	Tier         int                  `json:"tier"`
	Pinned       bool                 `json:"pinned,omitempty"`
	Status       string               `json:"status"`
	ReviewState  string               `json:"review_state,omitempty"`
	Confidence   float64              `json:"confidence"`
	SourceType   string               `json:"source_type"`
	Tags         []string             `json:"tags,omitempty"`
	Evidence     []store.EvidenceIn   `json:"evidence,omitempty"`
	Rationale    string               `json:"rationale,omitempty"`
	Details      json.RawMessage      `json:"details,omitempty"`
	ValidFrom    string               `json:"valid_from,omitempty"`
	ValidUntil   string               `json:"valid_until,omitempty"`
	ReviewAfter  string               `json:"review_after,omitempty"`
	ContentHash  string               `json:"content_hash,omitempty"`
}

type bundleLink struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Label    string `json:"label"`
}

type bundleVaultEntry struct {
	ScopeSlug string `json:"scope_slug"`
	Key       string `json:"key"`
	Label     string `json:"label,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Value     string `json:"value"`
}

// BundleImportReport summarises merge results.
type BundleImportReport struct {
	ProjectsCreated int      `json:"projects_created"`
	ProjectsSkipped int      `json:"projects_skipped"`
	MemoriesCreated int      `json:"memories_created"`
	MemoriesSkipped int      `json:"memories_skipped"`
	LinksCreated    int      `json:"links_created"`
	LinksSkipped    int      `json:"links_skipped"`
	VaultCreated    int      `json:"vault_created"`
	VaultUpdated    int      `json:"vault_updated"`
	VaultSkipped    int      `json:"vault_skipped"`
	AliasesCreated  int      `json:"aliases_created"`
	AliasesSkipped  int      `json:"aliases_skipped"`
	Errors          []string `json:"errors,omitempty"`
}

// ExportBundle builds a .impamind ZIP (admin / human only).
func (s *Service) ExportBundle(ctx context.Context, call Call, opts BundleExportOpts) ([]byte, string, error) {
	if err := s.humanOnly(call); err != nil {
		return nil, "", err
	}
	if !call.SS.IsAdmin() {
		return nil, "", newErr(http.StatusForbidden, "forbidden", "bundle export needs the admin session")
	}
	pass := strings.TrimSpace(opts.Passphrase)
	if len(pass) < 8 {
		return nil, "", newErr(http.StatusBadRequest, "invalid", "passphrase must be at least 8 characters")
	}

	scopes, err := s.St.ListScopes(ctx, call.SS)
	if err != nil {
		return nil, "", err
	}
	wantSlug := strings.ToLower(strings.TrimSpace(opts.Project))
	var projects []store.Scope
	var global store.Scope
	for _, sc := range scopes {
		if sc.Kind == "global" {
			global = sc
			continue
		}
		if sc.DeletedAt != "" {
			continue
		}
		if wantSlug != "" && sc.Slug != wantSlug {
			continue
		}
		projects = append(projects, sc)
	}
	if wantSlug != "" && len(projects) == 0 {
		return nil, "", newErr(http.StatusNotFound, "not_found", "project not found")
	}

	payload := bundlePayload{Projects: []bundleProject{}, Aliases: []bundleAlias{}, Memories: []bundleMemory{}, Links: []bundleLink{}}
	manifestScopes := []bundleScopeBrief{}
	scopeByID := map[string]store.Scope{}
	var scopeIDs []string

	for _, sc := range projects {
		scopeByID[sc.ID] = sc
		scopeIDs = append(scopeIDs, sc.ID)
		payload.Projects = append(payload.Projects, bundleProject{
			Slug: sc.Slug, Name: sc.Name, Description: sc.Description, Hidden: sc.Hidden,
			ReviewPolicy: sc.ReviewPolicy, Tags: sc.Tags,
		})
		manifestScopes = append(manifestScopes, bundleScopeBrief{Slug: sc.Slug, Kind: "project", Name: sc.Name})
		aliases, err := s.St.ListAliases(ctx, call.SS, sc.ID)
		if err != nil {
			return nil, "", err
		}
		for _, a := range aliases {
			payload.Aliases = append(payload.Aliases, bundleAlias{
				ProjectSlug: sc.Slug, Kind: a.Kind, Value: strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(a.Alias, "git:"), "path:"), "name:"),
			})
		}
	}
	if opts.IncludeGlobal && global.ID != "" {
		scopeByID[global.ID] = global
		scopeIDs = append(scopeIDs, global.ID)
		manifestScopes = append(manifestScopes, bundleScopeBrief{Slug: "global", Kind: "global", Name: global.Name})
	}

	var allMemIDs []string
	idToMem := map[string]store.Memory{}
	for _, sid := range scopeIDs {
		offset := 0
		for {
			list, total, err := s.St.ListMemories(ctx, call.SS, store.MemoryFilter{
				ScopeIDs: []string{sid}, Statuses: []string{"active"}, Limit: 500, Offset: offset,
			})
			if err != nil {
				return nil, "", err
			}
			for _, m := range list {
				if m.Kind == "checkpoint" {
					continue
				}
				full, err := s.St.GetMemory(ctx, call.SS, m.ID)
				if err != nil {
					full = m
				}
				slug := scopeByID[full.ScopeID].Slug
				if scopeByID[full.ScopeID].Kind == "global" {
					slug = "global"
				}
				bm := bundleMemory{
					ID: full.ID, ScopeSlug: slug, Kind: full.Kind, Title: full.Title, Content: full.Content,
					Tier: full.Tier, Pinned: full.Pinned, Status: full.Status, ReviewState: full.ReviewState,
					Confidence: full.Confidence, SourceType: full.SourceType, Tags: full.Tags,
					Rationale: full.Rationale, Details: full.Details, ValidFrom: full.ValidFrom,
					ValidUntil: full.ValidUntil, ReviewAfter: full.ReviewAfter, ContentHash: full.ContentHash,
				}
				for _, e := range full.Evidence {
					bm.Evidence = append(bm.Evidence, store.EvidenceIn{Type: e.Type, Ref: e.Ref, Note: e.Note})
				}
				payload.Memories = append(payload.Memories, bm)
				allMemIDs = append(allMemIDs, full.ID)
				idToMem[full.ID] = full
			}
			offset += len(list)
			if offset >= total || len(list) == 0 {
				break
			}
		}
	}

	if len(allMemIDs) > 0 {
		links, err := s.St.LinksFor(ctx, call.SS, allMemIDs)
		if err != nil {
			return nil, "", err
		}
		seen := map[string]bool{}
		for _, l := range links {
			if _, ok := idToMem[l.SourceID]; !ok {
				continue
			}
			if _, ok := idToMem[l.TargetID]; !ok {
				continue
			}
			key := l.SourceID + "\x00" + l.TargetID + "\x00" + l.Label
			if seen[key] {
				continue
			}
			seen[key] = true
			payload.Links = append(payload.Links, bundleLink{SourceID: l.SourceID, TargetID: l.TargetID, Label: l.Label})
		}
	}

	var vaultPlain []bundleVaultEntry
	for _, sid := range scopeIDs {
		list, err := s.St.ListVault(ctx, call.SS, []string{sid})
		if err != nil {
			return nil, "", err
		}
		sc := scopeByID[sid]
		slug := sc.Slug
		if sc.Kind == "global" {
			slug = "global"
		}
		for _, meta := range list {
			blob, err := s.St.GetVaultCipher(ctx, call.SS, sid, meta.Key)
			if err != nil {
				return nil, "", err
			}
			val, err := s.openVault(blob)
			if err != nil {
				return nil, "", err
			}
			vaultPlain = append(vaultPlain, bundleVaultEntry{
				ScopeSlug: slug, Key: meta.Key, Label: meta.Label, Hint: meta.Hint, Value: val,
			})
		}
	}
	if vaultPlain == nil {
		vaultPlain = []bundleVaultEntry{}
	}
	vaultJSON, err := json.Marshal(vaultPlain)
	if err != nil {
		return nil, "", err
	}
	salt, sealed, err := sealBundleVault(pass, vaultJSON)
	if err != nil {
		return nil, "", newErr(http.StatusBadRequest, "invalid", "could not seal vault with passphrase")
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	man := bundleManifest{
		Format: bundleFormat, FormatVersion: bundleFormatVersion,
		SchemaVersion: store.SchemaVersion, ExportedAt: store.FormatTime(time.Now().UTC()),
		IncludeGlobal: opts.IncludeGlobal, IncludeSessions: false,
		Scopes: manifestScopes,
		Counts: bundleCounts{
			Projects: len(payload.Projects), Memories: len(payload.Memories),
			Links: len(payload.Links), VaultEntries: len(vaultPlain), Aliases: len(payload.Aliases),
		},
		ContentHash: sha256Hex(payloadJSON, sealed),
	}
	manJSON, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manJSON},
		{"payload.json", payloadJSON},
		{"vault.salt", salt},
		{"vault.sealed", sealed},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, "", err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}

	name := "impamind-bundle.impamind"
	if wantSlug != "" {
		name = "impamind-" + wantSlug + ".impamind"
	} else {
		name = "impamind-full-" + time.Now().UTC().Format("20060102-150405") + ".impamind"
	}
	s.audit(call, "bundle_export", "", 200, fmt.Sprintf("projects=%d memories=%d vault=%d", man.Counts.Projects, man.Counts.Memories, man.Counts.VaultEntries))
	return buf.Bytes(), name, nil
}

// ImportBundle merges a .impamind ZIP into this instance (admin / human only).
func (s *Service) ImportBundle(ctx context.Context, call Call, raw []byte, opts BundleImportOpts) (BundleImportReport, error) {
	var rep BundleImportReport
	if err := s.humanOnly(call); err != nil {
		return rep, err
	}
	if !call.SS.IsAdmin() {
		return rep, newErr(http.StatusForbidden, "forbidden", "bundle import needs the admin session")
	}
	pass := strings.TrimSpace(opts.Passphrase)
	if len(pass) < 8 {
		return rep, newErr(http.StatusBadRequest, "invalid", "passphrase must be at least 8 characters")
	}
	if len(raw) == 0 {
		return rep, newErr(http.StatusBadRequest, "invalid", "empty bundle")
	}
	if len(raw) > 128<<20 {
		return rep, newErr(http.StatusBadRequest, "invalid", "bundle too large (max 128MiB)")
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return rep, newErr(http.StatusBadRequest, "invalid", "not a valid .impamind zip")
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return rep, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 96<<20))
		rc.Close()
		if err != nil {
			return rep, err
		}
		files[f.Name] = data
	}
	manJSON, ok1 := files["manifest.json"]
	payloadJSON, ok2 := files["payload.json"]
	salt, ok3 := files["vault.salt"]
	sealed, ok4 := files["vault.sealed"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return rep, newErr(http.StatusBadRequest, "invalid", "bundle missing manifest/payload/vault files")
	}
	var man bundleManifest
	if err := json.Unmarshal(manJSON, &man); err != nil {
		return rep, newErr(http.StatusBadRequest, "invalid", "bad manifest.json")
	}
	if man.Format != bundleFormat || man.FormatVersion != bundleFormatVersion {
		return rep, newErr(http.StatusBadRequest, "invalid", "unsupported bundle format")
	}
	if got := sha256Hex(payloadJSON, sealed); got != man.ContentHash {
		return rep, newErr(http.StatusBadRequest, "invalid", "content_hash mismatch (corrupt bundle?)")
	}
	var payload bundlePayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return rep, newErr(http.StatusBadRequest, "invalid", "bad payload.json")
	}
	vaultJSON, err := openBundleVault(pass, salt, sealed)
	if err != nil {
		return rep, newErr(http.StatusBadRequest, "invalid", "wrong passphrase or corrupt vault.sealed")
	}
	var vaultEntries []bundleVaultEntry
	if err := json.Unmarshal(vaultJSON, &vaultEntries); err != nil {
		return rep, newErr(http.StatusBadRequest, "invalid", "bad sealed vault payload")
	}

	// Refresh admin scope set after each project create.
	adminCall := call
	refresh := func() error {
		id, err := s.St.AdminIdentity(ctx)
		if err != nil {
			return err
		}
		g, projects, err := s.St.ScopeRefs(ctx)
		if err != nil {
			return err
		}
		ss, err := auth.Resolve(id.Principal, id.Grants, g, projects, auth.Request{AllProjects: true})
		if err != nil {
			return err
		}
		adminCall.SS = ss
		return nil
	}
	if err := refresh(); err != nil {
		return rep, err
	}

	slugToID := map[string]string{}
	if g := adminCall.SS.GlobalID(); g != "" {
		slugToID["global"] = g
	}
	existing, err := s.St.ListScopes(ctx, adminCall.SS)
	if err != nil {
		return rep, err
	}
	for _, sc := range existing {
		if sc.Kind == "project" && sc.DeletedAt == "" {
			slugToID[sc.Slug] = sc.ID
		}
	}

	for _, p := range payload.Projects {
		slug := strings.ToLower(strings.TrimSpace(p.Slug))
		if slug == "" || slug == "global" {
			continue
		}
		if _, ok := slugToID[slug]; ok {
			rep.ProjectsSkipped++
			continue
		}
		policy := p.ReviewPolicy
		if policy == "" {
			policy = "balanced"
		}
		sc, err := s.St.CreateProject(ctx, adminCall.SS, store.ProjectInput{
			Slug: slug, Name: p.Name, Description: p.Description, Hidden: p.Hidden,
			ReviewPolicy: policy, Tags: p.Tags,
		})
		if err != nil {
			rep.Errors = append(rep.Errors, "project "+slug+": "+err.Error())
			continue
		}
		slugToID[slug] = sc.ID
		rep.ProjectsCreated++
		if err := refresh(); err != nil {
			return rep, err
		}
	}

	for _, a := range payload.Aliases {
		sid, ok := slugToID[strings.ToLower(a.ProjectSlug)]
		if !ok {
			rep.AliasesSkipped++
			continue
		}
		if _, err := s.St.AddAlias(ctx, adminCall.SS, sid, a.Kind, a.Value); err != nil {
			rep.AliasesSkipped++
			continue
		}
		rep.AliasesCreated++
	}

	// Index existing memories for idempotency: by id and by title|hash
	type memKey struct{ title, hash string }
	existingByID := map[string]store.Memory{}
	existingByKey := map[string]map[memKey]store.Memory{}
	for slug, sid := range slugToID {
		existingByKey[slug] = map[memKey]store.Memory{}
		offset := 0
		for {
			list, total, err := s.St.ListMemories(ctx, adminCall.SS, store.MemoryFilter{
				ScopeIDs: []string{sid}, Statuses: []string{"active", "proposed"}, Limit: 500, Offset: offset,
			})
			if err != nil {
				return rep, err
			}
			for _, m := range list {
				existingByID[m.ID] = m
				existingByKey[slug][memKey{m.Title, m.ContentHash}] = m
			}
			offset += len(list)
			if offset >= total || len(list) == 0 {
				break
			}
		}
	}

	idMap := map[string]string{} // old -> new
	for _, bm := range payload.Memories {
		slug := strings.ToLower(strings.TrimSpace(bm.ScopeSlug))
		sid, ok := slugToID[slug]
		if !ok {
			rep.Errors = append(rep.Errors, "memory "+bm.Title+": unknown scope "+slug)
			rep.MemoriesSkipped++
			continue
		}
		hash := bm.ContentHash
		if hash == "" {
			hash = store.ContentHash(bm.Content)
		}
		if cur, ok := existingByID[bm.ID]; ok && cur.ScopeID == sid {
			idMap[bm.ID] = cur.ID
			rep.MemoriesSkipped++
			continue
		}
		if cur, ok := existingByKey[slug][memKey{bm.Title, hash}]; ok {
			idMap[bm.ID] = cur.ID
			rep.MemoriesSkipped++
			continue
		}

		src := bm.SourceType
		switch src {
		case "agent_inferred", "tool_observed", "imported", "human_ui":
		default:
			src = "imported"
		}
		kind := bm.Kind
		if !store.ValidKind(kind) {
			kind = "note"
		}
		scKind := "project"
		policy := "balanced"
		if slug == "global" {
			scKind = "global"
		} else if sc, err := s.St.GetScope(ctx, adminCall.SS, sid); err == nil {
			policy = sc.ReviewPolicy
		}
		status := "active"
		if store.Protected(scKind, policy, kind) {
			status = "proposed"
		}
		nm := store.NewMemory{
			ScopeID: sid, Kind: kind, Title: bm.Title, Content: bm.Content, Tier: bm.Tier,
			Pinned: bm.Pinned, Status: status, ReviewState: bm.ReviewState, Confidence: bm.Confidence,
			SourceType: src, Client: "bundle-import", Tags: bm.Tags, Evidence: bm.Evidence,
			Rationale: bm.Rationale, Details: bm.Details, ValidFrom: bm.ValidFrom,
			ValidUntil: bm.ValidUntil, ReviewAfter: bm.ReviewAfter,
		}
		m, err := s.St.CreateMemory(ctx, adminCall.SS, nm)
		if err != nil {
			rep.Errors = append(rep.Errors, "memory "+bm.Title+": "+err.Error())
			rep.MemoriesSkipped++
			continue
		}
		if status == "proposed" && adminCall.SS.CanApprove(sid) {
			if approved, err := s.St.Approve(ctx, adminCall.SS, m.ID, store.ApproveInput{Note: "imported from .impamind bundle"}); err == nil {
				m = approved
			}
		}
		idMap[bm.ID] = m.ID
		existingByID[m.ID] = m
		existingByKey[slug][memKey{m.Title, m.ContentHash}] = m
		rep.MemoriesCreated++
		s.index(m)
	}

	if opts.SkipLinks {
		rep.LinksSkipped = len(payload.Links)
	} else {
		const linkBudget = 2500 // keep reverse-proxy requests under ~100s
		links := payload.Links
		if len(links) > linkBudget {
			rep.Errors = append(rep.Errors, fmt.Sprintf("skipped %d links over budget of %d (pass skip_links or re-import later)", len(links)-linkBudget, linkBudget))
			rep.LinksSkipped += len(links) - linkBudget
			links = links[:linkBudget]
		}
		for _, l := range links {
			src, ok1 := idMap[l.SourceID]
			dst, ok2 := idMap[l.TargetID]
			if !ok1 || !ok2 {
				rep.LinksSkipped++
				continue
			}
			if _, err := s.St.CreateLink(ctx, adminCall.SS, src, dst, l.Label); err != nil {
				rep.LinksSkipped++
				continue
			}
			rep.LinksCreated++
		}
	}

	for _, v := range vaultEntries {
		slug := strings.ToLower(strings.TrimSpace(v.ScopeSlug))
		sid, ok := slugToID[slug]
		if !ok {
			rep.Errors = append(rep.Errors, "vault "+v.Key+": unknown scope "+slug)
			rep.VaultSkipped++
			continue
		}
		_, metaErr := s.St.GetVaultMeta(ctx, adminCall.SS, sid, v.Key)
		exists := metaErr == nil
		if exists && !opts.OverwriteVault {
			rep.VaultSkipped++
			continue
		}
		if _, err := s.VaultPut(ctx, adminCall, VaultPutIn{
			Scope: slug, Key: v.Key, Label: v.Label, Hint: v.Hint, Value: v.Value,
		}); err != nil {
			rep.Errors = append(rep.Errors, "vault "+slug+"/"+v.Key+": "+err.Error())
			rep.VaultSkipped++
			continue
		}
		if exists {
			rep.VaultUpdated++
		} else {
			rep.VaultCreated++
		}
	}

	s.audit(call, "bundle_import", "", 200, fmt.Sprintf("created_mem=%d vault=%d", rep.MemoriesCreated, rep.VaultCreated+rep.VaultUpdated))
	return rep, nil
}
