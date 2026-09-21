package handler

import (
	"fmt"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// JWT_SECRET is process configuration; changing it invalidates existing tokens.
func jwtSigningKey() ([]byte, error) {
	secret := os.Getenv("JWT_SECRET")
	if len([]byte(secret)) < 32 || strings.TrimSpace(secret) != secret {
		return nil, fmt.Errorf("JWT_SECRET must contain at least 32 bytes with no surrounding whitespace")
	}
	return []byte(secret), nil
}

// ValidateJWTConfig lets startup fail before opening listeners or dependencies.
func ValidateJWTConfig() error {
	_, err := jwtSigningKey()
	return err
}

func signToken(claims CustomClaims) (string, error) {
	key, err := jwtSigningKey()
	if err != nil {
		return "", err
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}
