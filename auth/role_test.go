package auth

import (
	"testing"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

func TestOIDCRoleIsDerivedFromCurrentGroupsAndCanDemote(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	authenticator := &Authenticator{
		Store: store,
		Cfg: &config.Config{
			OIDCAdminGroup: "instance-admins",
			OIDCUserGroup:  "",
		},
	}
	if got := authenticator.resolveRole("person@example.test", []string{"instance-admins"}); got != "admin" {
		t.Fatalf("admin group role=%q", got)
	}
	if _, err := authenticator.provisionUser("person@example.test", "admin", "https://id.example.test", "subject", "person", []string{"instance-admins"}); err != nil {
		t.Fatal(err)
	}
	if got := authenticator.resolveRole("person@example.test", nil); got != "user" {
		t.Fatalf("role without current admin group=%q, want user", got)
	}
	if _, err := authenticator.provisionUser("person@example.test", "user", "https://id.example.test", "subject", "person", nil); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("person")
	if err != nil || user == nil || user.Role != "user" {
		t.Fatalf("demoted user=%#v err=%v", user, err)
	}
}

func TestOIDCIdentityUsesIssuerAndSubjectInsteadOfMutableClaims(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	authenticator := &Authenticator{Store: store, Cfg: &config.Config{}}

	first, err := authenticator.provisionUser(
		"old-address@example.test", "user", "https://id.example.test", "stable-subject", "old-name", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authenticator.provisionUser(
		"new-address@example.test", "user", "https://id.example.test", "stable-subject", "new-name", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("mutable claims created a second user: first=%d second=%d", first.ID, second.ID)
	}
	if second.Email != "new-name" {
		t.Fatalf("canonical username was not updated from Authentik: %q", second.Email)
	}
	stored, err := store.GetUserByOIDCIdentity("https://id.example.test", "stable-subject")
	if err != nil || stored == nil || stored.PreferredUsername != "new-name" {
		t.Fatalf("stored OIDC identity=%#v err=%v", stored, err)
	}
}

func TestLoginDestinationUsesRoleAppropriateLandingPage(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{role: "admin", want: "/admin"},
		{role: "user", want: "/my-servers"},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			if got := loginDestination(tt.role); got != tt.want {
				t.Fatalf("loginDestination(%q)=%q, want %q", tt.role, got, tt.want)
			}
		})
	}
}
