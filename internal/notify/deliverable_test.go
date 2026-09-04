package notify

import "testing"

func TestIsDeliverable(t *testing.T) {
	cases := []struct {
		name    string
		address string
		want    bool
	}{
		// The address shape the simulator produces. This is the case that
		// caused the incident.
		{"simulator address", "sim.2026.7647@simulated.invalid", false},
		{"reserved invalid TLD", "someone@nowhere.invalid", false},
		{"reserved test TLD", "someone@library.test", false},
		{"reserved example TLD", "someone@thing.example", false},
		{"localhost", "root@localhost", false},
		{"example.com", "student@example.com", false},
		{"subdomain of example.com", "student@mail.example.com", false},

		{"real university address", "ada@oauife.edu.ng", true},
		{"real provider", "ada.okafor@gmail.com", true},
		{"project domain", "holibrary@appmd.dev", true},

		{"uppercase is still reserved", "SIM@SIMULATED.INVALID", false},
		{"surrounding spaces", "  sim@simulated.invalid  ", false},

		{"no at sign", "not-an-address", false},
		{"empty local part", "@example.org", false},
		{"empty domain", "someone@", false},
		{"empty string", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDeliverable(c.address); got != c.want {
				t.Errorf("IsDeliverable(%q) = %v, want %v", c.address, got, c.want)
			}
		})
	}
}
