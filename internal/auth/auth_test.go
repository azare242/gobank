package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestTokens(t *testing.T) {
	config := Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "api"}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.Generate(42)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.Validate(raw); err != nil || id != 42 {
		t.Fatalf("round trip: %d, %v", id, err)
	}
	now := time.Now()
	base := jwt.RegisteredClaims{Issuer: "test", Audience: jwt.ClaimStrings{"api"}, Subject: "42", ID: "id", IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}
	for _, name := range []string{"expired", "missing expiry", "issuer", "audience", "subject", "future", "algorithm", "signature"} {
		t.Run(name, func(t *testing.T) {
			claims := base
			method := jwt.SigningMethodHS256
			key := config.Key
			switch name {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			case "missing expiry":
				claims.ExpiresAt = nil
			case "issuer":
				claims.Issuer = "other"
			case "audience":
				claims.Audience = jwt.ClaimStrings{"other"}
			case "subject":
				claims.Subject = "0"
			case "future":
				claims.NotBefore = jwt.NewNumericDate(now.Add(time.Hour))
			case "algorithm":
				method = jwt.SigningMethodHS384
			case "signature":
				key = []byte(strings.Repeat("x", 32))
			}
			raw, err := jwt.NewWithClaims(method, claims).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Validate(raw); err == nil {
				t.Fatal("accepted invalid token")
			}
		})
	}
	if _, err := New(Config{Key: []byte("weak"), Issuer: "test", Audience: "api"}); err == nil {
		t.Fatal("accepted weak key")
	}
}
