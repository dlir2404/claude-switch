package profile

import (
	"testing"
)

func TestValidateProfileName(t *testing.T) {
	valid := []string{
		"personal",
		"work",
		"dev-team",
		"user_01",
		"user.name",
		"user@gmail.com",
		"user+test@domain.co.uk",
	}

	for _, name := range valid {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("expected %q to be valid, got: %v", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"../escape",
		"../../etc/passwd",
		"/absolute/path",
		"name with spaces",
		"name;rm -rf",
		"name$HOME",
		"name*glob",
		".hidden",
	}

	for _, name := range invalid {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("expected %q to be invalid, but passed validation", name)
		}
	}
}
