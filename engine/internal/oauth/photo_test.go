package oauth

import "testing"

func TestProfilePhoto(t *testing.T) {
	for in, want := range map[string]string{
		"https://lh3.googleusercontent.com/a/test-photo=s96-c": "https://lh3.googleusercontent.com/a/test-photo=s96-c",
		"http://lh3.googleusercontent.com/a/test-photo":        "",
		"https://evil.example/a.png":                           "",
		"https://googleusercontent.com.evil.example/a.png":     "",
		"javascript:alert(1)":                                  "",
		"":                                                     "",
	} {
		if got := profilePhoto(in); got != want {
			t.Errorf("profilePhoto(%q) = %q, want %q", in, got, want)
		}
	}
}
