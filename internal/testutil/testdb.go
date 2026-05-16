package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenTestDB connects to the test PostgreSQL database using settings from
// .env.test. テスト環境の接続情報を環境変数またはファイルから読み取る。
// 環境変数 TEST_DB_HOST, TEST_DB_PORT, TEST_DB_NAME, TEST_DB_USER,
// TEST_DB_PASS, TEST_DB_SSLMODE が設定されていればそちらを優先する。
func OpenTestDB() (*gorm.DB, error) {
	loadEnvFile()

	host := EnvOrDefault("TEST_DB_HOST", "localhost")
	port := EnvOrDefault("TEST_DB_PORT", "5432")
	name := EnvOrDefault("TEST_DB_NAME", "misskey_test")
	user := EnvOrDefault("TEST_DB_USER", "mk")
	pass := EnvOrDefault("TEST_DB_PASS", "mk")
	sslmode := EnvOrDefault("TEST_DB_SSLMODE", "disable")

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, pass, name, sslmode)

	// PreferSimpleProtocol: pgx の auto-prepared statement cache を無効化する。
	// 共有 testDB を多 test で再利用する CI 環境で、軽微な schema 差分 (= AutoMigrate
	// による additive 変更や test 経路で DDL 相当が走るケース) のあと再 query すると
	// `ERROR: cached plan must not change result type` が偶発的に出る (#1089)。
	// simple protocol に倒すと server 側 prepared statement を作らないため本問題が
	// 構造的に消える。性能は落ちるが test では問題ない。
	return gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}

// ApplyMigrations applies all up migration SQL files to the database.
// 冪等な操作 (IF NOT EXISTS) なので複数回呼んでも安全。
func ApplyMigrations(db *gorm.DB) {
	files, err := findMigrationFiles()
	if err != nil {
		panic("failed to find migration files: " + err.Error())
	}
	for _, path := range files {
		sql, err := os.ReadFile(path)
		if err != nil {
			panic("failed to read migration file: " + err.Error())
		}
		if err := db.Exec(string(sql)).Error; err != nil {
			// 冪等なDDLのエラーは無視 (既にテーブルが存在する場合等)
			_ = err
		}
	}
}

func findMigrationFiles() ([]string, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	dir := filepath.Join(projectRoot, "migration")
	matches, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// MustOpenTestDB は OpenTestDB のパニック版。init() で使う。
func MustOpenTestDB() *gorm.DB {
	db, err := OpenTestDB()
	if err != nil {
		panic("failed to open test DB: " + err.Error())
	}
	return db
}

// EnvOrDefault returns the environment variable value or the fallback.
func EnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadEnvFile loads .env.test from the project root (best-effort).
func loadEnvFile() {
	// プロジェクトルートの .env.test を探す
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	// testutil/ → internal/ → project root
	root := filepath.Join(filepath.Dir(file), "..", "..")
	envPath := filepath.Join(root, ".env.test")

	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// 既に環境変数が設定されていればスキップ
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
