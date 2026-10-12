package protocol

import (
	"context"
	"testing"

	"impamind/internal/config"
	"impamind/internal/store"
)

func TestBundleExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	svc.Cfg = config.Config{Secret: "bundle-test-secret-key-32b!!!!", BackupDir: t.TempDir(), ObservationBudget: 15}
	admin := adminSS(t, st, true)
	sc := openProject(t, st, admin, "bundle-demo")
	admin = adminSS(t, st, true)
	c := Call{SS: admin, Profile: ProfileUI, Client: "test"}

	m1 := memory(t, st, admin, sc.ID, "bundle fact one")
	m2 := memory(t, st, admin, sc.ID, "bundle fact two")
	if _, err := st.CreateLink(ctx, admin, m1.ID, m2.ID, "related"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VaultPut(ctx, c, VaultPutIn{
		Scope: sc.Slug, Key: "api-token", Label: "API", Hint: "demo", Value: "secret-value-xyz",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAlias(ctx, admin, sc.ID, "name", "Bundle Demo Alias"); err != nil {
		t.Fatal(err)
	}

	raw, name, err := svc.ExportBundle(ctx, c, BundleExportOpts{
		Passphrase: "test-pass-12", IncludeGlobal: false, Project: sc.Slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 100 || name == "" {
		t.Fatalf("empty export name=%q len=%d", name, len(raw))
	}

	// Fresh instance
	st2, svc2 := newSvc(t)
	svc2.Cfg = config.Config{Secret: "other-instance-secret-key!!!!!", BackupDir: t.TempDir(), ObservationBudget: 15}
	admin2 := adminSS(t, st2, true)
	c2 := Call{SS: admin2, Profile: ProfileUI, Client: "test"}

	rep, err := svc2.ImportBundle(ctx, c2, raw, BundleImportOpts{Passphrase: "test-pass-12", OverwriteVault: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProjectsCreated != 1 || rep.MemoriesCreated < 2 {
		t.Fatalf("report %+v", rep)
	}
	if rep.VaultCreated+rep.VaultUpdated < 1 {
		t.Fatalf("vault not imported %+v", rep)
	}

	admin2 = adminSS(t, st2, true)
	c2.SS = admin2
	got, err := svc2.VaultGet(ctx, c2, VaultKeyIn{Scope: "bundle-demo", Key: "api-token"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "secret-value-xyz" {
		t.Fatalf("vault value %q", got.Value)
	}

	// Idempotent re-import
	rep2, err := svc2.ImportBundle(ctx, c2, raw, BundleImportOpts{Passphrase: "test-pass-12"})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.MemoriesCreated != 0 {
		t.Fatalf("expected skip on reimport %+v", rep2)
	}

	// Wrong passphrase
	if _, err := svc2.ImportBundle(ctx, c2, raw, BundleImportOpts{Passphrase: "wrong-pass"}); err == nil {
		t.Fatal("expected wrong passphrase error")
	}

	_ = store.SchemaVersion
}
