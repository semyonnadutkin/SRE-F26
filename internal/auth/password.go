package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const PasswordCost = 12

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

func NormalizeUsername(value string) (string, error) {
	username := strings.TrimSpace(value)
	if !usernamePattern.MatchString(username) {
		return "", errors.New("username must contain 3–32 letters, numbers, or underscores")
	}
	return strings.ToLower(username), nil
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) {
		return errors.New("password contains invalid characters")
	}
	if utf8.RuneCountInString(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	// Reject bcrypt's byte limit explicitly rather than truncate a password.
	if len(password) > 72 {
		return errors.New("password is too long")
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	return string(hash), err
}

func PasswordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
