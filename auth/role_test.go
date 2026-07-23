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
	if err := authenticator.provisionUser("person@example.test", "admin", "subject", "Person", []string{"instance-admins"}); err != nil {
		t.Fatal(err)
	}
	if got := authenticator.resolveRole("person@example.test", nil); got != "user" {
		t.Fatalf("role without current admin group=%q, want user", got)
	}
	if err := authenticator.provisionUser("person@example.test", "user", "subject", "Person", nil); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByEmail("person@example.test")
	if err != nil || user == nil || user.Role != "user" {
		t.Fatalf("demoted user=%#v err=%v", user, err)
	}
}
