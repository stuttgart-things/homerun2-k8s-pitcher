package profile

import "testing"

func TestSelfNamespace(t *testing.T) {
	t.Run("prefers POD_NAMESPACE", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "homerun2-k8s-pitcher-pr-7")
		if got := SelfNamespace(); got != "homerun2-k8s-pitcher-pr-7" {
			t.Errorf("SelfNamespace() = %q, want %q", got, "homerun2-k8s-pitcher-pr-7")
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "  homerun2  \n")
		if got := SelfNamespace(); got != "homerun2" {
			t.Errorf("SelfNamespace() = %q, want %q", got, "homerun2")
		}
	})

	t.Run("falls back to default off-cluster", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "")
		// No service-account file off-cluster, so this resolves to "default".
		if got := SelfNamespace(); got != "default" {
			t.Errorf("SelfNamespace() = %q, want %q", got, "default")
		}
	})
}

func TestResolveNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "my-ns")

	tests := []struct {
		name   string
		specNs string
		want   string
	}{
		{"self sentinel", "@self", "my-ns"},
		{"star means all", "*", ""},
		{"empty means all", "", ""},
		{"literal namespace", "kube-system", "kube-system"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveNamespace(tc.specNs); got != tc.want {
				t.Errorf("ResolveNamespace(%q) = %q, want %q", tc.specNs, got, tc.want)
			}
		})
	}
}

func TestResolveSecretNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "my-ns")

	tests := []struct {
		name string
		ns   string
		want string
	}{
		{"empty resolves to self", "", "my-ns"},
		{"self sentinel resolves to self", "@self", "my-ns"},
		{"literal namespace unchanged", "vault", "vault"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveSecretNamespace(tc.ns); got != tc.want {
				t.Errorf("ResolveSecretNamespace(%q) = %q, want %q", tc.ns, got, tc.want)
			}
		})
	}
}
