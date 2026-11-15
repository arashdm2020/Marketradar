package main

import "testing"

func TestPublicAdminURLValidation(t *testing.T) {
	t.Setenv("ADMIN_PUBLIC_URL", "https://admin-radar.foxwd.tech/login")
	got, e := publicAdminURL()
	if e != nil || got != "https://admin-radar.foxwd.tech/login" {
		t.Fatalf("valid production URL rejected: %q %v", got, e)
	}
	if root := publicAdminRoot(got); root != "https://admin-radar.foxwd.tech/" {
		t.Fatalf("unexpected public admin root: %s", root)
	}
	for _, invalid := range []string{
		"http://admin-radar.foxwd.tech/login",
		"https://user:pass@admin-radar.foxwd.tech/login",
		"https://admin-radar.foxwd.tech/login?token=secret",
	} {
		t.Run(invalid, func(t *testing.T) {
			t.Setenv("ADMIN_PUBLIC_URL", invalid)
			if _, e := publicAdminURL(); e == nil {
				t.Fatalf("unsafe URL accepted: %s", invalid)
			}
		})
	}
}
