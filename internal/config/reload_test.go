package config

import "testing"

func TestReloadResultRoundTrip(t *testing.T) {
	path := t.TempDir() + "/run/reload.json"
	want := ReloadResult{Rev: 7, Path: "config.yaml", Changed: []string{"server"}, RestartRequired: []string{"server.addr"}}
	if err := WriteReloadResult(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReloadResult(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rev != want.Rev || got.Path != want.Path || len(got.RestartRequired) != 1 {
		t.Fatalf("result=%+v, want %+v", got, want)
	}
}
