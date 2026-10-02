package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestUsernamePolicy(t *testing.T) {
	username, err := NormalizeUsername(" Alice_42 ")
	if err != nil || username != "alice_42" {
		t.Fatalf("normalization: %q, %v", username, err)
	}
	for _, input := range []string{"", "ab", "a/b", "alice@example.test", "with space", "\u212aelvin", strings.Repeat("x", 33)} {
		if _, err := NormalizeUsername(input); err == nil {
			t.Errorf("invalid username accepted: %q", input)
		}
	}
}

func TestPasswordSaltAndVerification(t *testing.T) {
	password := "a reasonable test password"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password hashes must use different salts")
	}
	cost, err := bcrypt.Cost([]byte(first))
	if err != nil || cost != PasswordCost {
		t.Fatalf("unexpected cost: %d, %v", cost, err)
	}
	if !PasswordMatches(first, password) || PasswordMatches(first, "a different test password") {
		t.Fatal("password verification failed")
	}
}

func TestPasswordCharacterAndByteLimits(t *testing.T) {
	for _, password := range []string{strings.Repeat("a", 7), strings.Repeat("\u00e9", 7), strings.Repeat("a", 73), strings.Repeat("\u00e9", 37), string([]byte{0xff})} {
		if ValidatePassword(password) == nil {
			t.Fatal("invalid password accepted")
		}
	}
	for _, password := range []string{strings.Repeat("a", 8), strings.Repeat("\u00e9", 8), strings.Repeat("\U0001f512", 8), strings.Repeat("a", 72), strings.Repeat("\u00e9", 36)} {
		if err := ValidatePassword(password); err != nil {
			t.Fatal(err)
		}
	}
}
