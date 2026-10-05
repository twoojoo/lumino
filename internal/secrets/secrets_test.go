package secrets

import "testing"

func TestSecrets(t *testing.T) {
	UseMock()
	if !Available() {
		t.Fatal("mock keyring unavailable")
	}
	if _, ok := Get("c1", Password); ok {
		t.Fatal("unexpected secret")
	}
	if err := Set("c1", Password, "pw", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	memory = map[string]string{} // a new session: only the keyring remains
	mu.Unlock()
	if v, ok := Get("c1", Password); !ok || v != "pw" {
		t.Fatalf("persisted secret %q %v", v, ok)
	}
	if err := Set("c1", Password, "temp", false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	memory = map[string]string{}
	mu.Unlock()
	if _, ok := Get("c1", Password); ok {
		t.Fatal("unsaved secret persisted (or old copy kept)")
	}
	_ = Set("c1", SSH, "x", true)
	DeleteAll("c1")
	if _, ok := Get("c1", SSH); ok {
		t.Fatal("DeleteAll left a secret")
	}
}
