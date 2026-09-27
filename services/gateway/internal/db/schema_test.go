package db

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// Runs only against a real, disposable Postgres:
//
//	docker run --rm -d -p 55432:5432 -e POSTGRES_PASSWORD=x postgres:15-alpine
//	OPTIFUSE_TEST_DATABASE_URL=postgres://postgres:x@localhost:55432/postgres go test ./services/gateway/internal/db/
func TestNew_AppliesSchemaConcurrentlyAndIdempotently(t *testing.T) {
	url := os.Getenv("OPTIFUSE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPTIFUSE_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Several replicas starting at once against an empty database: the case
	// the advisory lock exists for. Without it, CREATE EXTENSION races and one
	// of these fails with a unique-constraint violation on pg_extension.
	const replicas = 4
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := New(ctx, url)
			if err != nil {
				errs <- err
				return
			}
			p.pool.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent New failed: %v", err)
	}

	// And once more against the now-populated database.
	p, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New against an existing schema failed: %v", err)
	}
	defer p.pool.Close()

	for _, table := range []string{"users", "profiles", "tokens"} {
		var exists bool
		if err := p.pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists); err != nil || !exists {
			t.Errorf("table %s missing after New (err=%v)", table, err)
		}
	}
}
