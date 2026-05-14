package prompt

import (
	"os"
	"testing"
)

func TestString_NonInteractive_UsesEnv(t *testing.T) {
	t.Setenv("PLATFORMCTL_PORKBUN_API_KEY", "from-env")
	got, err := String("porkbun api key", "PLATFORMCTL_PORKBUN_API_KEY", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestString_NonInteractive_MissingEnv(t *testing.T) {
	os.Unsetenv("PLATFORMCTL_TEST_MISSING")
	_, err := String("label", "PLATFORMCTL_TEST_MISSING", true)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestSecret_NonInteractive_UsesEnv(t *testing.T) {
	t.Setenv("PLATFORMCTL_SECRET_VAL", "mysecret")
	got, err := Secret("secret label", "PLATFORMCTL_SECRET_VAL", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mysecret" {
		t.Fatalf("got %q", got)
	}
}

func TestConfirm_NonInteractive_ReturnsDefault(t *testing.T) {
	got, err := Confirm("proceed?", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expected default true")
	}
}
