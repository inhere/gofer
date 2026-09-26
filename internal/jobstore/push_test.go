package jobstore

import "testing"

func TestPushSubscriptionStoreCRUD(t *testing.T) {
	store := openTest(t)
	alice := PushSubscription{
		CallerID: "alice", Endpoint: "https://push.example.test/device-1",
		P256DH: "alice-p256dh", Auth: "alice-auth", UserAgent: "browser-a",
		CreatedAt: 10,
	}
	if err := store.UpsertPushSubscription(alice); err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	got, err := store.ListPushSubscriptions("alice")
	if err != nil || len(got) != 1 {
		t.Fatalf("list alice = %#v, %v", got, err)
	}
	if got[0] != alice {
		t.Fatalf("alice round trip = %#v, want %#v", got[0], alice)
	}

	// The endpoint is globally unique. Registering the same browser endpoint for a
	// new caller transfers ownership and resets delivery health.
	bob := alice
	bob.CallerID = "bob"
	bob.P256DH = "bob-p256dh"
	bob.Auth = "bob-auth"
	bob.UserAgent = "browser-b"
	bob.CreatedAt = 20
	if err := store.UpsertPushSubscription(bob); err != nil {
		t.Fatalf("upsert bob: %v", err)
	}
	aliceRows, err := store.ListPushSubscriptions("alice")
	if err != nil || len(aliceRows) != 0 {
		t.Fatalf("alice rows after transfer = %#v, %v", aliceRows, err)
	}
	bobRows, err := store.ListPushSubscriptions("bob")
	if err != nil || len(bobRows) != 1 || bobRows[0].P256DH != "bob-p256dh" || bobRows[0].LastOKAt != 0 {
		t.Fatalf("bob rows = %#v, %v", bobRows, err)
	}

	if ok, err := store.DeletePushSubscription("alice", bob.Endpoint); err != nil || ok {
		t.Fatalf("alice delete bob endpoint = %v, %v; want false,nil", ok, err)
	}
	if err := store.MarkPushSubscriptionOK(bob.Endpoint, 30); err != nil {
		t.Fatalf("mark ok: %v", err)
	}
	bobRows, _ = store.ListPushSubscriptions("bob")
	if bobRows[0].LastOKAt != 30 {
		t.Fatalf("last_ok_at = %d, want 30", bobRows[0].LastOKAt)
	}
	if ok, err := store.DeletePushSubscription("bob", bob.Endpoint); err != nil || !ok {
		t.Fatalf("bob delete = %v, %v; want true,nil", ok, err)
	}

	gone := bob
	gone.Endpoint = "https://push.example.test/gone"
	gone.CreatedAt = 40
	if err := store.UpsertPushSubscription(gone); err != nil {
		t.Fatalf("upsert gone: %v", err)
	}
	if err := store.DeletePushSubscriptionEndpoint(gone.Endpoint); err != nil {
		t.Fatalf("delete invalid endpoint: %v", err)
	}
	bobRows, _ = store.ListPushSubscriptions("bob")
	if len(bobRows) != 0 {
		t.Fatalf("invalid endpoint survived: %#v", bobRows)
	}
}
