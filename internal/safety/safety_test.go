//go:build dev

package safety

import "testing"

func TestDevRefusesUserKubeconfig(t *testing.T) {
	for _, p := range []string{"/home/x/.kube/config", "/tmp/kubeconfig"} {
		if _, err := ResolveKubeconfig(p); err == nil {
			t.Fatalf("expected %q to be refused", p)
		}
	}
	if _, err := ResolveKubeconfig("/repo/.dev/kubeconfig"); err != nil {
		t.Fatal(err)
	}
}

func TestDevRefusesForeignContexts(t *testing.T) {
	if CheckContext("prod-eu") == nil {
		t.Fatal("prod-eu must be refused")
	}
	if err := CheckContext("kind-kubeon-dev"); err != nil {
		t.Fatal(err)
	}
	if got := FilterContexts([]string{"a", "kind-kubeon-dev"}); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}
