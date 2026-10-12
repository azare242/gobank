package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/azare242/gobank/internal/models"
	"gorm.io/gorm"
)

type Sessions struct {
	DB  *gorm.DB
	TTL time.Duration
}
type Login struct {
	Token     string `json:"session_token"`
	ExpiresIn int64  `json:"expires_in"`
}

func tokenHash(token string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(token) != 43 || len(raw) != 32 {
		return "", Fail(401, "unauthorized", "Unauthorized")
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), nil
}
func (s Sessions) Issue(ctx context.Context, id uint, audit models.AuditLog) (Login, error) {
	var result Login
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return result, err
	}
	result = Login{base64.RawURLEncoding.EncodeToString(raw), int64(s.TTL.Seconds())}
	hash, _ := tokenHash(result.Token)
	now := time.Now().UTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.Session{TokenHash: hash, UserID: id, CreatedAt: now, ExpiresAt: now.Add(s.TTL)}).Error; err != nil {
			return err
		}
		audit.UserID = &id
		audit.Result = "success"
		audit.StatusCode = 200
		return tx.Create(&audit).Error
	})
	if err != nil {
		return Login{}, err
	}
	return result, nil
}
func (s Sessions) Validate(ctx context.Context, token string) (uint, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return 0, err
	}
	var session models.Session
	err = s.DB.WithContext(ctx).Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).First(&session).Error
	if err == nil {
		var count int64
		err = s.DB.WithContext(ctx).Model(&models.User{}).Where("id = ?", session.UserID).Count(&count).Error
		if err == nil && count == 0 {
			err = gorm.ErrRecordNotFound
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = Fail(401, "unauthorized", "Unauthorized")
	}
	return session.UserID, err
}
func (s Sessions) Logout(ctx context.Context, token string) error {
	hash, err := tokenHash(token)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Where("token_hash = ?", hash).Delete(&models.Session{}).Error
}
func (s Sessions) RevokeAll(ctx context.Context, id uint) error {
	return s.DB.WithContext(ctx).Where("user_id = ?", id).Delete(&models.Session{}).Error
}
func (s Sessions) Cleanup(ctx context.Context) error {
	return s.DB.WithContext(ctx).Exec("DELETE FROM sessions WHERE id IN (SELECT id FROM sessions WHERE expires_at <= ? ORDER BY id LIMIT 256)", time.Now().UTC()).Error
}
func (s Sessions) RunCleanup(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Cleanup(ctx); err != nil && ctx.Err() == nil && report != nil {
				report(err)
			}
		}
	}
}
