package testutil

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDB holds a test PostgreSQL container and GORM connection.
type TestDB struct {
	Container testcontainers.Container
	DB        *gorm.DB
}

// SetupPostgres starts a PostgreSQL container and returns a GORM connection.
// The migration SQL is automatically applied.
func SetupPostgres(ctx context.Context) (*TestDB, error) {
	container, err := tcpostgres.Run(ctx,
		"postgres:18-alpine",
		tcpostgres.WithDatabase("misskey_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start postgres container: %w", err)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("failed to get connection string: %w", err)
	}

	db, err := gorm.Open(postgres.Open(connStr), &gorm.Config{
		Logger:                   logger.Default.LogMode(logger.Silent),
		DisableNestedTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to test db: %w", err)
	}

	// マイグレーションSQLを適用
	ApplyMigrations(db)

	return &TestDB{Container: container, DB: db}, nil
}

// Teardown stops the container.
func (t *TestDB) Teardown(ctx context.Context) {
	if t.Container != nil {
		_ = t.Container.Terminate(ctx)
	}
}

// TruncateAll truncates all data tables for test isolation.
func (t *TestDB) TruncateAll() {
	tables := []string{
		"user_note_pining", "poll", "note_reaction", "access_token",
		"follow_request", "following",
		"note", "user_keypair", "user_profile", "emoji",
		"drive_file", "drive_folder", "instance", "meta", `"user"`,
	}
	for _, table := range tables {
		t.DB.Exec(fmt.Sprintf("DELETE FROM %s", table))
	}
}

// TestRedis holds a test Redis container and client.
type TestRedis struct {
	Container testcontainers.Container
	Client    *redis.Client
	Addr      string
}

// SetupRedis starts a Redis container and returns a client.
func SetupRedis(ctx context.Context) (*TestRedis, error) {
	container, err := tcredis.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("6379/tcp").WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start redis container: %w", err)
	}

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get redis connection string: %w", err)
	}

	opts, err := redis.ParseURL(connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis url: %w", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return &TestRedis{
		Container: container,
		Client:    client,
		Addr:      opts.Addr,
	}, nil
}

// Teardown stops the container.
func (t *TestRedis) Teardown(ctx context.Context) {
	if t.Client != nil {
		_ = t.Client.Close()
	}
	if t.Container != nil {
		_ = t.Container.Terminate(ctx)
	}
}

// FlushAll clears all Redis data.
func (t *TestRedis) FlushAll(ctx context.Context) {
	t.Client.FlushAll(ctx)
}

// Host returns the Redis host.
func (t *TestRedis) Host() string {
	host, _, _ := net.SplitHostPort(t.Addr)
	return host
}

// Port returns the Redis port.
func (t *TestRedis) Port() int {
	_, port, _ := net.SplitHostPort(t.Addr)
	p, _ := strconv.Atoi(port)
	return p
}

// SkipIfNoDocker skips the test if Docker is not available.
func SkipIfNoDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" {
		// ローカル環境ではDockerの有無を確認
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		provider, err := testcontainers.ProviderDocker.GetProvider()
		if err != nil {
			t.Skip("Docker is not available, skipping integration test")
		}
		err = provider.Health(ctx)
		if err != nil {
			t.Skip("Docker is not healthy, skipping integration test")
		}
	}
}
