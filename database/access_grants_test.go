package database

import "testing"

func TestServerAccessDecisionUsesExplicitDenyPrecedence(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "acl-test", GameType: "example", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	server, _ = store.GetServerByName(server.Name)
	for _, grant := range []*ServerAccessGrant{
		{ServerID: server.ID, SubjectType: "everyone", Subject: "*", Effect: "allow", Capabilities: []string{"view"}},
		{ServerID: server.ID, SubjectType: "authenticated", Subject: "*", Effect: "allow", Capabilities: []string{"console.read"}},
		{ServerID: server.ID, SubjectType: "group", Subject: "ReadOnly", Effect: "deny", Capabilities: []string{"console.read"}},
	} {
		if err := store.SetServerAccessGrant(grant); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := store.EvaluateServerAccess(server.ID, "reader@example.test", []string{"ReadOnly"}, "console.read")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || !decision.Governed || decision.Reason != "explicit deny from group:ReadOnly" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	view, err := store.EvaluateServerAccess(server.ID, "reader@example.test", []string{"ReadOnly"}, "view")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Allowed {
		t.Fatalf("view should remain allowed: %#v", view)
	}
	publicView, err := store.EvaluateServerAccess(server.ID, "anonymous", nil, "view")
	if err != nil || !publicView.Allowed {
		t.Fatalf("everyone grant should include public visitors: %#v err=%v", publicView, err)
	}
}

func TestServerAccessDecisionDeniesByDefault(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "default-deny", GameType: "example", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	server, _ = store.GetServerByName(server.Name)
	decision, err := store.EvaluateServerAccess(server.ID, "user@example.test", nil, "view")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.Governed {
		t.Fatalf("server without grants must deny: %#v", decision)
	}
}
