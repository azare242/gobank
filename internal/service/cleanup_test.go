package service

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
)

func TestSessionCleanupBoundAndCancellation(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now().UTC()
	rows := make([]models.Session, 301)
	for i := range rows {
		rows[i] = models.Session{TokenHash: fmt.Sprintf("%064d", i), UserID: 1, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}
	}
	rows[300].ExpiresAt = now.Add(time.Hour)
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	sessions := Sessions{DB: db, TTL: time.Minute}
	if err := sessions.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var expired int64
	if err := db.Model(&models.Session{}).Where("expires_at <= ?", now).Count(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if expired != 44 {
		t.Fatalf("cleanup removed %d expired sessions, want 256", 300-expired)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		sessions.RunCleanup(ctx, 5*time.Millisecond, func(err error) {
			select {
			case failures <- err:
			default:
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("cleanup goroutine did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := db.Model(&models.Session{}).Where("expires_at <= ?", now).Count(&expired).Error; err != nil {
			t.Fatal(err)
		}
		if expired == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic cleanup did not remove remaining expired sessions")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled cleanup did not exit")
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	var active models.Session
	if err := db.First(&active, rows[300].ID).Error; err != nil {
		t.Fatalf("cleanup removed an active session: %v", err)
	}
}
