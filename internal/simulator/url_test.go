package simulator

import "testing"

// The simulator sends a librarian bearer token to whatever it is pointed at, so
// the target address decides who receives staff credentials (DEF-025).
func TestSafeTargetURL(t *testing.T) {
	cases := []struct {
		in      string
		allowed bool
	}{
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"https://holibrary.example.edu.ng", true},
		{"http://attacker.example.com", false},
		{"http://192.168.1.50:8080", false},
		{"ftp://localhost", false},
	}
	for _, c := range cases {
		_, err := safeTargetURL(c.in)
		if (err == nil) != c.allowed {
			t.Errorf("safeTargetURL(%q) allowed=%v, want %v", c.in, err == nil, c.allowed)
		}
	}
}
