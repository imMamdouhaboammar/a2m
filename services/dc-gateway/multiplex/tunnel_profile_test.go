package multiplex

import "testing"

func TestTunnelProfileNameDefaultsAndOverrides(t *testing.T) {
	t.Setenv("A2M_TUNNEL_PROFILE_1", "")
	t.Setenv("A2M_TUNNEL_PROFILE_2", "")
	if got := tunnelProfileName(1); got != "a2m" {
		t.Fatalf("primary default: got %q, want a2m", got)
	}
	if got := tunnelProfileName(2); got != "a2m-2" {
		t.Fatalf("secondary default: got %q, want a2m-2", got)
	}
	t.Setenv("A2M_TUNNEL_PROFILE_1", "custom-primary")
	t.Setenv("A2M_TUNNEL_PROFILE_2", "custom-secondary")
	if got := tunnelProfileName(1); got != "custom-primary" {
		t.Fatalf("primary override: got %q", got)
	}
	if got := tunnelProfileName(2); got != "custom-secondary" {
		t.Fatalf("secondary override: got %q", got)
	}
}
