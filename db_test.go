package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

func TestValidDatabaseType(t *testing.T) {
	for _, ok := range []string{"sqlite", "mysql", "postgres"} {
		if !validDatabaseType(ok) {
			t.Errorf("%q should be a valid database type", ok)
		}
	}
	for _, bad := range []string{"", "postgresql", "pg", "sqlite3", "SQLite"} {
		if validDatabaseType(bad) {
			t.Errorf("%q should not be a valid database type", bad)
		}
	}
}

func TestDBConfigFromEnv_Postgres(t *testing.T) {
	env := map[string]string{
		"database": "postgres", "POSTGRES_HOST": "db.internal", "POSTGRES_PORT": "5433",
		"POSTGRES_USER": "goblog", "POSTGRES_PASSWORD": "s3cret", "POSTGRES_DATABASE": "blog", "POSTGRES_SSLMODE": "require",
	}
	cfg := dbConfigFromEnv(env)
	if cfg.Type != "postgres" || cfg.Host != "db.internal" || cfg.Port != "5433" || cfg.User != "goblog" || cfg.Password != "s3cret" || cfg.Name != "blog" || cfg.SSLMode != "require" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	// Values are single-quoted so passwords containing spaces or '=' survive.
	dsn := cfg.dsn()
	for _, want := range []string{"host='db.internal'", "port='5433'", "user='goblog'", "password='s3cret'", "dbname='blog'", "sslmode='require'"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("postgres dsn missing %q: %s", want, dsn)
		}
	}
	quoted := dbConfig{Type: "postgres", Password: `it's a=pass word`}.dsn()
	if !strings.Contains(quoted, `password='it\'s a=pass word'`) {
		t.Errorf("expected the password to be quoted and escaped, got %s", quoted)
	}
}

func TestDBConfigFromEnv_PostgresDefaults(t *testing.T) {
	cfg := dbConfigFromEnv(map[string]string{"database": "postgres", "POSTGRES_HOST": "h", "POSTGRES_USER": "u", "POSTGRES_PASSWORD": "p", "POSTGRES_DATABASE": "d"})
	if cfg.Port != "5432" || cfg.SSLMode != "disable" {
		t.Errorf("expected port 5432 and sslmode disable by default, got port=%q sslmode=%q", cfg.Port, cfg.SSLMode)
	}
}

func TestDBConfigFromEnv_MySQLAndSQLiteUnchanged(t *testing.T) {
	my := dbConfigFromEnv(map[string]string{"database": "mysql", "MYSQL_HOST": "h", "MYSQL_PORT": "3306", "MYSQL_USER": "u", "MYSQL_PASSWORD": "p", "MYSQL_DATABASE": "d"})
	if my.dsn() != "u:p@tcp(h:3306)/d?charset=utf8mb4&parseTime=True&loc=Local" {
		t.Errorf("mysql dsn changed: %s", my.dsn())
	}
	lite := dbConfigFromEnv(map[string]string{"database": "sqlite", "sqlite_db": "../db"})
	if lite.Type != "sqlite" || lite.SQLiteFile != "../db" {
		t.Errorf("unexpected sqlite config: %+v", lite)
	}
}

// What the wizard writes to .env must parse back into the same config.
func TestDBConfig_EnvRoundTrip(t *testing.T) {
	for _, cfg := range []dbConfig{
		{Type: "sqlite", SQLiteFile: "goblog.db"},
		{Type: "mysql", Host: "h", Port: "3306", User: "u", Password: "p", Name: "d"},
		{Type: "postgres", Host: "h", Port: "5432", User: "u", Password: "p w", Name: "d", SSLMode: "disable"},
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(cfg.envFile()), 0600); err != nil {
			t.Fatal(err)
		}
		env, err := godotenv.Read(path)
		if err != nil {
			t.Fatalf("%s: env written by envFile() must be parseable: %v\n%s", cfg.Type, err, cfg.envFile())
		}
		if got := dbConfigFromEnv(env); got != cfg {
			t.Errorf("%s: round trip mismatch\n got %+v\nwant %+v", cfg.Type, got, cfg)
		}
	}
}

func TestOpenDatabase_SQLite(t *testing.T) {
	db, err := openDatabase(dbConfig{Type: "sqlite", SQLiteFile: filepath.Join(t.TempDir(), "t.db")})
	if err != nil || db == nil {
		t.Fatalf("expected sqlite to open, got db=%v err=%v", db, err)
	}
	if name := db.Dialector.Name(); name != "sqlite" {
		t.Errorf("expected sqlite dialector, got %s", name)
	}
}

func TestOpenDatabase_RejectsUnknownType(t *testing.T) {
	if _, err := openDatabase(dbConfig{Type: "oracle"}); err == nil {
		t.Fatal("expected an error for an unknown database type")
	}
}

// Runs only when a Postgres instance is available (CI provides one).
func TestOpenDatabase_Postgres(t *testing.T) {
	dsn := os.Getenv("GOBLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOBLOG_TEST_POSTGRES_DSN not set")
	}
	cfg := dbConfigFromDSNForTest(dsn)
	db, err := openDatabase(cfg)
	if err != nil {
		t.Fatalf("expected postgres to open with %+v: %v", cfg, err)
	}
	if name := db.Dialector.Name(); name != "postgres" {
		t.Errorf("expected postgres dialector, got %s", name)
	}
}

// dbConfigFromDSNForTest parses "host=.. port=.. user=.. password=.. dbname=.. sslmode=.."
// so the Postgres test can be pointed at any instance via one env var.
func dbConfigFromDSNForTest(dsn string) dbConfig {
	cfg := dbConfig{Type: "postgres", Port: "5432", SSLMode: "disable"}
	for _, kv := range strings.Fields(dsn) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "host":
			cfg.Host = v
		case "port":
			cfg.Port = v
		case "user":
			cfg.User = v
		case "password":
			cfg.Password = v
		case "dbname":
			cfg.Name = v
		case "sslmode":
			cfg.SSLMode = v
		}
	}
	return cfg
}
