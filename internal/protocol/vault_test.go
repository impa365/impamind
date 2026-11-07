package protocol

import (
	"context"
	"testing"

	"impamind/internal/config"
)

func TestVaultRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, svc := newSvc(t)
	svc.Cfg = config.Config{Secret: "test-vault-secret-key-32bytes!!"}
	admin := adminSS(t, st, true)
	sc := openProject(t, st, admin, "vaulted")
	admin = adminSS(t, st, true)
	c := Call{SS: admin, Profile: ProfileUI, Client: "test"}

	put, err := svc.VaultPut(ctx, c, VaultPutIn{
		Scope: sc.Slug, Key: "docker-hub", Label: "Docker Hub", Hint: "org impa365",
		Value: "super-secret-token-value-xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if put.Ref != "vault:vaulted/docker-hub" || put.Key != "docker-hub" {
		t.Fatalf("put %+v", put)
	}
	list, err := svc.VaultList(ctx, c, VaultListIn{Scope: sc.Slug})
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	if list[0].Hint != "org impa365" {
		t.Fatalf("hint %q", list[0].Hint)
	}
	// list must not leak value — struct has no Value field; check JSON-ish via get
	got, err := svc.VaultGet(ctx, c, VaultKeyIn{Scope: sc.Slug, Key: "docker-hub"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "super-secret-token-value-xyz" {
		t.Fatalf("got %q", got.Value)
	}
	if err := svc.VaultDelete(ctx, c, VaultKeyIn{Scope: sc.Slug, Key: "docker-hub"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VaultGet(ctx, c, VaultKeyIn{Scope: sc.Slug, Key: "docker-hub"}); err == nil {
		t.Fatal("expected not found after delete")
	}
}
