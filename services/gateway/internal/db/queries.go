package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps pgxpool and provides all database operations
// the gateway needs. No ORM — just plain SQL.
type Pool struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, connString string) (*Pool, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return &Pool{pool: pool}, nil
}

func (p *Pool) Close() {
	p.pool.Close()
}

// ── Domain types ──────────────────────────────────────────────────────────────

type User struct {
	ID        int64
	Username  string
	Email     string
	CreatedAt time.Time
}

type Profile struct {
	ID                int64
	UserID            int64
	GitHubAccessToken string
	AWSRoleARN        string
	AWSExternalID     string // UUID as string
	Subscription      string
}

// ── User queries ──────────────────────────────────────────────────────────────

// GetOrCreateUser finds a user by GitHub username or creates one.
// Python: User.objects.get_or_create(username=username)
func (p *Pool) GetOrCreateUser(ctx context.Context, username, email string) (*User, bool, error) {
	// Try to find existing user first.
	user, err := p.GetUserByUsername(ctx, username)
	if err == nil {
		return user, false, nil
	}

	// Create new user.
	user = &User{}
	err = p.pool.QueryRow(ctx,
		`INSERT INTO users (username, email)
		 VALUES ($1, $2)
		 ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id, username, email, created_at`,
		username, email,
	).Scan(&user.ID, &user.Username, &user.Email, &user.CreatedAt)
	if err != nil {
		return nil, false, fmt.Errorf("GetOrCreateUser: %w", err)
	}
	return user, true, nil
}

func (p *Pool) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	user := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, username, email, created_at FROM users WHERE username = $1`,
		username,
	).Scan(&user.ID, &user.Username, &user.Email, &user.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("GetUserByUsername: %w", err)
	}
	return user, nil
}

// ── Profile queries ───────────────────────────────────────────────────────────

// GetOrCreateProfile finds or creates a profile for the given user.
// Python: Profile.objects.get_or_create(user=user)
func (p *Pool) GetOrCreateProfile(ctx context.Context, userID int64, githubToken string) (*Profile, error) {
	profile := &Profile{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO profiles (user_id, github_access_token)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET github_access_token = EXCLUDED.github_access_token
		 RETURNING id, user_id, github_access_token,
		           COALESCE(aws_role_arn, ''), aws_external_id::text, subscription`,
		userID, githubToken,
	).Scan(
		&profile.ID, &profile.UserID, &profile.GitHubAccessToken,
		&profile.AWSRoleARN, &profile.AWSExternalID, &profile.Subscription,
	)
	if err != nil {
		return nil, fmt.Errorf("GetOrCreateProfile: %w", err)
	}
	return profile, nil
}

func (p *Pool) GetProfileByUserID(ctx context.Context, userID int64) (*Profile, error) {
	profile := &Profile{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, user_id, github_access_token,
		        COALESCE(aws_role_arn, ''), aws_external_id::text, subscription
		 FROM profiles WHERE user_id = $1`,
		userID,
	).Scan(
		&profile.ID, &profile.UserID, &profile.GitHubAccessToken,
		&profile.AWSRoleARN, &profile.AWSExternalID, &profile.Subscription,
	)
	if err != nil {
		return nil, fmt.Errorf("GetProfileByUserID: %w", err)
	}
	return profile, nil
}

// UpdateAWSRoleARN updates the user's AWS role ARN.
// Python: profile.aws_role_arn = aws_role_arn; profile.save()
func (p *Pool) UpdateAWSRoleARN(ctx context.Context, userID int64, roleARN string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE profiles SET aws_role_arn = $1 WHERE user_id = $2`,
		roleARN, userID,
	)
	if err != nil {
		return fmt.Errorf("UpdateAWSRoleARN: %w", err)
	}
	return nil
}

// ── Token queries ─────────────────────────────────────────────────────────────

// GetOrCreateToken returns the existing token for a user or creates a new one.
// Python: Token.objects.get_or_create(user=user)
func (p *Pool) GetOrCreateToken(ctx context.Context, userID int64) (string, error) {
	// Check if token already exists.
	var key string
	err := p.pool.QueryRow(ctx,
		`SELECT key FROM tokens WHERE user_id = $1`, userID,
	).Scan(&key)
	if err == nil {
		return key, nil
	}

	// Generate a new 40-char hex token — same format as DRF.
	key, err = generateToken()
	if err != nil {
		return "", fmt.Errorf("GetOrCreateToken generate: %w", err)
	}

	_, err = p.pool.Exec(ctx,
		`INSERT INTO tokens (key, user_id) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET key = EXCLUDED.key`,
		key, userID,
	)
	if err != nil {
		return "", fmt.Errorf("GetOrCreateToken insert: %w", err)
	}
	return key, nil
}

// GetUserByToken looks up the user associated with a token.
// Called on every authenticated request by the auth middleware.
// Python: TokenAuthentication — DRF does this internally.
func (p *Pool) GetUserByToken(ctx context.Context, token string) (*User, error) {
	user := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT u.id, u.username, u.email, u.created_at
		 FROM users u
		 JOIN tokens t ON t.user_id = u.id
		 WHERE t.key = $1`,
		token,
	).Scan(&user.ID, &user.Username, &user.Email, &user.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("GetUserByToken: %w", err)
	}
	return user, nil
}

// generateToken creates a random 40-character hex string.
// Matches DRF's token format exactly.
func generateToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
