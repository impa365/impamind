package auth

import (
	"errors"
	"testing"
)

func refs() (ScopeRef, []ScopeRef) {
	g := ScopeRef{ID: "g", Slug: "global", Kind: "global"}
	return g, []ScopeRef{
		{ID: "a", Slug: "alpha", Kind: "project"},
		{ID: "b", Slug: "beta", Kind: "project"},
	}
}

func agent() Principal { return Principal{ID: "p", Class: AgentToken, Label: "agent"} }

func TestResolvePinsAndRejectsWidening(t *testing.T) {
	g, projects := refs()
	grants := []Grant{{ScopeID: "a", CanRead: true, CanWrite: true}, {ScopeID: "g", CanRead: true}}
	ss, err := Resolve(agent(), grants, g, projects, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if ss.Project().Slug != "alpha" {
		t.Fatalf("single-project token pinned to %q", ss.Project().Slug)
	}
	if ss.CanRead("b") || ss.CanRead("missing") {
		t.Fatal("pinned token can read outside its project")
	}
	if _, err := Resolve(agent(), grants, g, projects, Request{Arg: "beta"}); !errors.Is(err, ErrNoProject) {
		t.Fatalf("explicit other project: %v", err)
	}
	if _, err := Resolve(agent(), grants, g, projects, Request{Header: "beta"}); !errors.Is(err, ErrNoProject) {
		t.Fatalf("header other project: %v", err)
	}
	if _, err := Resolve(agent(), grants, g, projects, Request{Header: "alpha", Arg: "beta"}); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("header/arg conflict: %v", err)
	}
}

func TestResolveHeaderOnlyNarrows(t *testing.T) {
	g, projects := refs()
	grants := []Grant{
		{ScopeID: Wildcard, CanRead: true, CanWrite: true},
		{ScopeID: "g", CanRead: true},
	}
	ss, err := Resolve(agent(), grants, g, projects, Request{Header: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !ss.CanRead("a") || !ss.CanRead("g") || ss.CanRead("b") {
		t.Fatalf("header alpha reads %v", ss.ReadIDs())
	}
	if ss.Project().Slug != "alpha" {
		t.Fatalf("project %q", ss.Project().Slug)
	}
}

func TestResolveAllProjectsNeedsCross(t *testing.T) {
	g, projects := refs()
	grants := []Grant{{ScopeID: Wildcard, CanRead: true, CanWrite: true}}
	if _, err := Resolve(agent(), grants, g, projects, Request{AllProjects: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("all projects without cross: %v", err)
	}
	grants[0].CrossProject = true
	ss, err := Resolve(agent(), grants, g, projects, Request{AllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ss.CanRead("a") || !ss.CanRead("b") {
		t.Fatalf("cross read %v", ss.ReadIDs())
	}
}

func TestAgentCannotApprove(t *testing.T) {
	g, projects := refs()
	ss, err := Resolve(agent(), []Grant{{ScopeID: "g", CanRead: true, CanApprove: true}}, g, projects, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if ss.CanApprove("g") || len(ss.ApprovableIDs()) != 0 {
		t.Fatal("agent grant can_approve was honored")
	}
	human := Principal{ID: "h", Class: HumanToken, Label: "human"}
	ss, err = Resolve(human, []Grant{{ScopeID: "g", CanRead: true, CanApprove: true}}, g, projects, Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !ss.CanApprove("g") {
		t.Fatal("human can_approve was dropped")
	}
}

func TestZeroScopeSetReadsNothing(t *testing.T) {
	var ss ScopeSet
	if ss.ReadIDs() != nil || ss.CanRead("a") || ss.CanWrite("a") || ss.CanPropose("a") || ss.CanApprove("a") || ss.CanCreateProjects() {
		t.Fatal("zero ScopeSet grants access")
	}
}
