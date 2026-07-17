package identitypg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dgunzy/go-book/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSessionLifecycle(t *testing.T) {
	databaseURL := os.Getenv("IDENTITYPG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("IDENTITYPG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := time.Now().UTC().UnixNano()
	email := fmt.Sprintf("identity-integration-%d@example.test", suffix)
	var userID string
	err = pool.QueryRow(ctx, `INSERT INTO users (display_name, email, status) VALUES ('Identity Integration', $1, 'active') RETURNING id::text`, email).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM sessions WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_identities WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM memberships WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1::uuid`, userID)
	}()
	if _, err = pool.Exec(ctx, `INSERT INTO memberships (user_id, role) VALUES ($1::uuid, 'admin')`, userID); err != nil {
		t.Fatal(err)
	}

	service, err := identity.NewService(Store{Pool: pool}, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := identity.ParseProvider("google")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.CompleteSignIn(ctx, identity.VerifiedIdentity{
		Provider: provider, Subject: fmt.Sprintf("integration-subject-%d", suffix),
		Email: email, EmailVerified: true, DisplayName: "Identity Integration",
	})
	if err != nil {
		t.Fatalf("CompleteSignIn: %v", err)
	}
	if string(issued.Principal.User.ID) != userID {
		t.Fatalf("signed in user = %s, want %s", issued.Principal.User.ID, userID)
	}
	resumed, err := service.Resume(ctx, issued.Token.Value())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := identity.ValidateCSRF(resumed.Session, issued.CSRFSecret.Value()); err != nil {
		t.Fatalf("ValidateCSRF: %v", err)
	}

	rotated, err := service.Rotate(ctx, issued.Token.Value(), "integration rotation")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := service.Resume(ctx, issued.Token.Value()); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("old token after rotation error = %v", err)
	}
	if _, err := service.Resume(ctx, rotated.Token.Value()); err != nil {
		t.Fatalf("rotated token Resume: %v", err)
	}
	if err := service.Revoke(ctx, rotated.Token.Value(), "integration logout"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := service.Resume(ctx, rotated.Token.Value()); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked token error = %v", err)
	}
}
