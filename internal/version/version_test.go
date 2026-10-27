package version

import (
	"regexp"
	"testing"
)

func TestVersionIsSemantic(t *testing.T) {
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`).MatchString(String()) {
		t.Fatalf("%q is not a semantic version", String())
	}
}
