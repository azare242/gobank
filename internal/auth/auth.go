// Package auth issues access tokens and provides reusable Fiber authentication.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
)

const AccessTTL = 15 * time.Minute

type Config struct {
	Key      []byte
	Issuer   string
	Audience string
}

type Service struct{ config Config }

// FromEnv requires a base64-encoded, randomly generated key of at least 32 bytes.
func FromEnv() (*Service, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("JWT_SECRET"))
	if err != nil {
		return nil, errors.New("JWT_SECRET must be a base64-encoded random key")
	}
	return New(Config{Key: key, Issuer: os.Getenv("JWT_ISSUER"), Audience: os.Getenv("JWT_AUDIENCE")})
}

func New(config Config) (*Service, error) {
	if len(config.Key) < 32 {
		return nil, errors.New("JWT signing key must contain at least 32 random bytes")
	}
	if strings.TrimSpace(config.Issuer) == "" || strings.TrimSpace(config.Audience) == "" {
		return nil, errors.New("JWT issuer and audience are required")
	}
	config.Key = append([]byte(nil), config.Key...)
	return &Service{config: config}, nil
}

func (s *Service) Generate(userID uint) (string, error) {
	if userID == 0 {
		return "", errors.New("invalid user ID")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer: s.config.Issuer, Audience: jwt.ClaimStrings{s.config.Audience},
		Subject: strconv.FormatUint(uint64(userID), 10), ID: base64.RawURLEncoding.EncodeToString(id),
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.config.Key)
}

func (s *Service) Validate(raw string) (uint, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(_ *jwt.Token) (any, error) { return s.config.Key, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(s.config.Issuer), jwt.WithAudience(s.config.Audience),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithStrictDecoding())
	if err != nil || !token.Valid {
		return 0, errors.New("invalid access token")
	}
	id, err := strconv.ParseUint(claims.Subject, 10, strconv.IntSize)
	if err != nil || id == 0 || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ID == "" {
		return 0, errors.New("invalid access token claims")
	}
	return uint(id), nil
}

type identityKey struct{}

// UserID returns the identity established by Middleware; authorization is separate.
func UserID(c fiber.Ctx) (uint, bool) {
	id, ok := c.Locals(identityKey{}).(uint)
	return id, ok
}

func (s *Service) Middleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		parts := strings.Fields(c.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
			c.Set("WWW-Authenticate", "Bearer")
			return fiber.NewError(fiber.StatusUnauthorized, "authentication required")
		}
		id, err := s.Validate(parts[1])
		if err != nil {
			c.Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired access token")
		}
		c.Locals(identityKey{}, id)
		return c.Next()
	}
}
