package creds

import "testing"

func TestDecodeBackupValue(t *testing.T) {
	const blob = `{"claudeAiOauth":{}}`
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", blob, blob},
		{"base64", "go-keyring-base64:eyJjbGF1ZGVBaU9hdXRoIjp7fX0=", blob},
		{"hex", "go-keyring-encoded:7b22636c6175646541694f61757468223a7b7d7d", blob},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeBackupValue(c.in)
			if err != nil {
				t.Fatalf("decodeBackupValue(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("decodeBackupValue(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDecodeBackupValueRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		"go-keyring-base64:!!!not-base64!!!",
		"go-keyring-encoded:zz",
	} {
		if _, err := decodeBackupValue(in); err == nil {
			t.Errorf("decodeBackupValue(%q): expected error, got nil", in)
		}
	}
}
