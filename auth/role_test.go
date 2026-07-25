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
	if got := authenticator.resolveRole([]string{"instance-admins"}); got != "admin" {
		t.Fatalf("admin group role=%q", got)
	}
	if _, err := authenticator.provisionUser("admin", "https://id.example.test", "subject", "person", []string{"instance-admins"}); err != nil {
		t.Fatal(err)
	}
	if got := authenticator.resolveRole(nil); got != "user" {
		t.Fatalf("role without current admin group=%q, want user", got)
	}
	if _, err := authenticator.provisionUser("user", "https://id.example.test", "subject", "person", nil); err != nil {
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
		"user", "https://id.example.test", "stable-subject", "old-name", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authenticator.provisionUser(
		"user", "https://id.example.test", "stable-subject", "new-name", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("mutable claims created a second user: first=%d second=%d", first.ID, second.ID)
	}
	if second.Username != "new-name" {
		t.Fatalf("canonical username was not updated from Authentik: %q", second.Username)
	}
	stored, err := store.GetUserByOIDCIdentity("https://id.example.test", "stable-subject")
	if err != nil || stored == nil || stored.PreferredUsername != "new-name" {
		t.Fatalf("stored OIDC identity=%#v err=%v", stored, err)
	}
}

func TestOIDCRequiresPreferredUsername(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	authenticator := &Authenticator{Store: store, Cfg: &config.Config{}}

	if _, err := authenticator.provisionUser(
		"user", "https://id.example.test", "stable-subject", "", nil,
	); err == nil {
		t.Fatal("OIDC identity without preferred_username was accepted")
	}
}

func TestOIDCAdoptsSCIMIdentityOnlyByStableSubject(t *testing.T) {
	store, err := database.NewStore(t.TempDir() + "/hogs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	authenticator := &Authenticator{Store: store, Cfg: &config.Config{}}

	user, err := store.CreateUser("authentik-name", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserSCIM(user.ID, "stable-subject", "Authentik User", true); err != nil {
		t.Fatal(err)
	}
	adopted, err := authenticator.provisionUser(
		"user", "https://id.example.test", "stable-subject", "authentik-name", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.ID != user.ID || adopted.OIDCSubject != "stable-subject" {
		t.Fatalf("adopted user=%#v", adopted)
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
