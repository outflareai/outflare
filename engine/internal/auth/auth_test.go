package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if !CheckPassword(h, "correct horse battery") {
		t.Fatal("right password rejected")
	}
	if CheckPassword(h, "wrong horse battery") {
		t.Fatal("wrong password accepted")
	}
	if CheckPassword("garbage", "x") {
		t.Fatal("malformed hash accepted")
	}
}

func TestWeakPasswordRejected(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatalf("err = %v", err)
	}
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	a, ha := NewToken(PrefixAPIToken)
	b, _ := NewToken(PrefixAPIToken)
	if a == b || !strings.HasPrefix(a, PrefixAPIToken) {
		t.Fatalf("bad tokens %q %q", a, b)
	}
	if string(HashToken(a)) != string(ha) {
		t.Fatal("hash mismatch")
	}
}

func TestRoleScopes(t *testing.T) {
	for _, s := range RoleScopes(RoleViewer) {
		if s == ScopeMembersManage || s == ScopeTokensManage {
			t.Fatalf("viewer has %s", s)
		}
	}
	if !ValidRole(RoleOwner) || ValidRole("god") {
		t.Fatal("ValidRole wrong")
	}
}

func TestManageIncludesRead(t *testing.T) {
	for _, c := range []struct {
		scopes []string
		want   string
		ok     bool
	}{
		{[]string{ScopeLeadsManage}, ScopeLeadsRead, true},
		{[]string{ScopeMissionsRun}, ScopeMissionsRead, true},
		{[]string{ScopeInfraApprove}, ScopeInfraRead, true},
		{[]string{ScopeLeadsRead}, ScopeLeadsManage, false},
		{[]string{ScopeLeadsManage}, ScopeMailboxesRead, false},
		{[]string{ScopeSuppressionsManage}, ScopeLeadsRead, false},
		{nil, ScopeWorkspaceRead, false},
		// Everyone's AI accounts include your own, not the other way round.
		{[]string{ScopeAIAccountsRead}, ScopeMyAIAccountsRead, true},
		{[]string{ScopeAIAccountsManage}, ScopeMyAIAccountsRead, true},
		{[]string{ScopeAIAccountsManage}, ScopeMyAIAccountsManage, true},
		{[]string{ScopeAIAccountsRead}, ScopeMyAIAccountsManage, false},
		{[]string{ScopeMyAIAccountsManage}, ScopeMyAIAccountsRead, true},
		{[]string{ScopeMyAIAccountsManage}, ScopeAIAccountsRead, false},
		{[]string{ScopeAISpendRead}, ScopeAIAccountsRead, false},
	} {
		if got := Grants(c.scopes, c.want); got != c.ok {
			t.Errorf("Grants(%v, %s) = %v", c.scopes, c.want, got)
		}
	}
}

func TestRenamedScopes(t *testing.T) {
	old := []string{"projects.read", "projects.manage"}
	if !ValidScope("projects.manage") || !Grants([]string{"projects.manage"}, ScopeProfilesManage) || !Grants([]string{"projects.manage"}, ScopeProfilesRead) {
		t.Fatal("projects.manage should still grant profiles")
	}
	if !Grants([]string{ScopeProfilesRead}, "projects.read") || Grants([]string{"projects.read"}, ScopeProfilesManage) {
		t.Fatal("old and new names should mean the same scope")
	}
	if got := CleanScopes(append(old, ScopeProfilesRead, ScopeLeadsRead)); strings.Join(got, ",") != "leads.read,profiles.read,profiles.manage" {
		t.Fatalf("CleanScopes = %v", got)
	}
	if !Within(old, []string{ScopeProfilesRead, ScopeProfilesManage}) || !Within([]string{ScopeProfilesManage}, old) || Within(old, []string{ScopeProfilesRead}) {
		t.Fatal("Within should treat old names as the new ones")
	}
	if got := Rename([]string{"projects.read", "nope"}); strings.Join(got, ",") != "profiles.read,nope" {
		t.Fatalf("Rename = %v", got)
	}
}
