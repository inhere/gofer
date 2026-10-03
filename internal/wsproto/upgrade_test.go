package wsproto

import "testing"

func TestUpgradeProtocolCapability(t *testing.T) {
	if UpgradeMinProtocolVersion != 15 || CurrentProtocolVersion < UpgradeMinProtocolVersion {
		t.Fatalf("upgrade protocol floor/current = %d/%d, want floor 15 and current >= floor", UpgradeMinProtocolVersion, CurrentProtocolVersion)
	}
	if SupportsUpgrade(UpgradeMinProtocolVersion-1) || !SupportsUpgrade(UpgradeMinProtocolVersion) {
		t.Fatal("upgrade capability negotiation is not gated at v15")
	}
}

func TestUpgradeFrameRoundTrip(t *testing.T) {
	raw, err := EncodeFrame(TypeUpgrade, "", Upgrade{RequestID: "r1", SHA256: "abc", Size: 3, Version: "v2", URLPath: "/v1/workers/w1/upgrade/file"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil || env.Type != TypeUpgrade {
		t.Fatalf("decode=%+v err=%v", env, err)
	}
	got, err := As[Upgrade](env)
	if err != nil || got.RequestID != "r1" || got.Size != 3 {
		t.Fatalf("upgrade=%+v err=%v", got, err)
	}
}
