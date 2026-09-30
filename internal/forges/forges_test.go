package forges

import (
	"errors"
	"testing"
)

// A forge's base address is an absolute http or https address with nothing but a host; its kind must be known.
func TestValidate(t *testing.T) {
	Register("testkind", Kind{})
	for base, ok := range map[string]bool{
		"https://github.com":               true,
		"https://git.example.com/":         true,
		"http://127.0.0.1:8080":            true,
		"github.com":                       false,
		"ftp://git.example.com":            false,
		"https://git.example.com/api":      false,
		"https://user:pw@git.example.com":  false,
		"https://git.example.com/?x=1":     false,
		"https://git.example.com/#section": false,
	} {
		err := validate("testkind", "A forge", base)
		var invalid ErrInvalid
		if ok != (err == nil) || (!ok && !errors.As(err, &invalid)) {
			t.Errorf("%s: got %v", base, err)
		}
	}
	if err := validate("no-such-kind", "A forge", "https://github.com"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	if err := validate("testkind", " ", "https://github.com"); err == nil {
		t.Error("an empty name was accepted")
	}
	if normalize("https://git.example.com/") != "https://git.example.com" {
		t.Error("the trailing slash stayed")
	}
}
