package books

import "testing"

// The catalogue address decides what this server fetches. Configuration is not
// the same as trustworthy (DEF-024).
func TestSafeBaseURL(t *testing.T) {
	cases := []struct {
		in      string
		allowed bool
		why     string
	}{
		{"", true, "empty falls back to the real catalogue"},
		{"https://openlibrary.org", true, "the expected host"},
		{"http://localhost:8080", true, "local stub for tests"},
		{"http://169.254.169.254/latest/meta-data/", false, "cloud metadata service"},
		{"http://10.0.0.5:8080", false, "private network address"},
		{"https://evil.example.com", false, "an attacker's host"},
		{"file:///etc/passwd", false, "not an http scheme"},
		{"http://openlibrary.org", false, "plain http to a public host"},
		{"https://openlibrary.org.evil.com", false, "lookalike domain"},
	}
	for _, c := range cases {
		_, err := safeBaseURL(c.in)
		if (err == nil) != c.allowed {
			t.Errorf("%s: safeBaseURL(%q) allowed=%v, want %v (%s)",
				c.why, c.in, err == nil, c.allowed, c.why)
		}
	}
}
