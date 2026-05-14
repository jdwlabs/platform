package bootstrap

import (
	"testing"
)

const validRcloneBlock = `[gdrive]
type = drive
scope = drive
token = {"access_token":"x","token_type":"Bearer","refresh_token":"r","expiry":"2030-01-01T00:00:00Z"}
`

func TestValidateRcloneBlock_OK(t *testing.T) {
	if err := validateRcloneBlock(validRcloneBlock); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestValidateRcloneBlock_MissingSection(t *testing.T) {
	in := `[remote]
type = drive
token = {"access_token":"x","refresh_token":"r"}
`
	if err := validateRcloneBlock(in); err == nil {
		t.Fatal("expected error for missing [gdrive]")
	}
}

func TestValidateRcloneBlock_MissingToken(t *testing.T) {
	in := "[gdrive]\ntype = drive\n"
	if err := validateRcloneBlock(in); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestValidateRcloneBlock_InvalidTokenJSON(t *testing.T) {
	in := "[gdrive]\ntoken = not-json\n"
	if err := validateRcloneBlock(in); err == nil {
		t.Fatal("expected error for invalid token JSON")
	}
}

func TestValidateRcloneBlock_MissingRefreshToken(t *testing.T) {
	in := `[gdrive]
token = {"access_token":"x"}
`
	if err := validateRcloneBlock(in); err == nil {
		t.Fatal("expected error for missing refresh_token")
	}
}
