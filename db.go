package main

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// dbConfig is everything needed to open the blog database. It is read from
// .env at startup and from the install wizard's form, and written back to
// .env by the wizard, so the three stay in sync through this one type.
type dbConfig struct {
	Type       string // "sqlite", "mysql" or "postgres"
	SQLiteFile string
	Host       string
	Port       string
	User       string
	Password   string
	Name       string
	SSLMode    string // postgres only
}

// validDatabaseType reports whether t is a supported value for the
// "database" setting.
func validDatabaseType(t string) bool {
	switch t {
	case "sqlite", "mysql", "postgres":
		return true
	}
	return false
}

// dbConfigFromEnv reads the database settings from a parsed .env map.
func dbConfigFromEnv(env map[string]string) dbConfig {
	cfg := dbConfig{Type: env["database"]}
	switch cfg.Type {
	case "sqlite":
		cfg.SQLiteFile = env["sqlite_db"]
	case "mysql":
		cfg.Host, cfg.Port = env["MYSQL_HOST"], env["MYSQL_PORT"]
		cfg.User, cfg.Password, cfg.Name = env["MYSQL_USER"], env["MYSQL_PASSWORD"], env["MYSQL_DATABASE"]
	case "postgres":
		cfg.Host, cfg.Port = env["POSTGRES_HOST"], env["POSTGRES_PORT"]
		cfg.User, cfg.Password, cfg.Name = env["POSTGRES_USER"], env["POSTGRES_PASSWORD"], env["POSTGRES_DATABASE"]
		cfg.SSLMode = env["POSTGRES_SSLMODE"]
		if cfg.Port == "" {
			cfg.Port = "5432"
		}
		if cfg.SSLMode == "" {
			cfg.SSLMode = "disable"
		}
	}
	return cfg
}

// dbConfigFromForm reads the database settings from the install wizard form.
func dbConfigFromForm(c *gin.Context) dbConfig {
	cfg := dbConfig{Type: c.PostForm("dbtype")}
	switch cfg.Type {
	case "sqlite":
		cfg.SQLiteFile = c.PostForm("sqlite_file")
		if cfg.SQLiteFile == "" {
			cfg.SQLiteFile = c.PostForm("sqlite_db") // older field name
		}
	case "mysql":
		cfg.Host, cfg.Port = c.PostForm("mysql_host"), c.PostForm("mysql_port")
		cfg.User, cfg.Password, cfg.Name = c.PostForm("mysql_user"), c.PostForm("mysql_pass"), c.PostForm("mysql_db")
	case "postgres":
		cfg.Host, cfg.Port = c.PostForm("postgres_host"), c.PostForm("postgres_port")
		cfg.User, cfg.Password, cfg.Name = c.PostForm("postgres_user"), c.PostForm("postgres_pass"), c.PostForm("postgres_db")
		cfg.SSLMode = c.PostForm("postgres_sslmode")
		if cfg.Port == "" {
			cfg.Port = "5432"
		}
		if cfg.SSLMode == "" {
			cfg.SSLMode = "disable"
		}
	}
	return cfg
}

// envFile renders the settings as the .env lines the wizard writes.
func (cfg dbConfig) envFile() string {
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, envQuote(v)) }
	line("database", cfg.Type)
	switch cfg.Type {
	case "sqlite":
		line("sqlite_db", cfg.SQLiteFile)
	case "mysql":
		line("MYSQL_HOST", cfg.Host)
		line("MYSQL_PORT", cfg.Port)
		line("MYSQL_USER", cfg.User)
		line("MYSQL_PASSWORD", cfg.Password)
		line("MYSQL_DATABASE", cfg.Name)
	case "postgres":
		line("POSTGRES_HOST", cfg.Host)
		line("POSTGRES_PORT", cfg.Port)
		line("POSTGRES_USER", cfg.User)
		line("POSTGRES_PASSWORD", cfg.Password)
		line("POSTGRES_DATABASE", cfg.Name)
		line("POSTGRES_SSLMODE", cfg.SSLMode)
	}
	return b.String()
}

// envQuote renders a .env value: line breaks are stripped so a value can
// never add lines to the file, and values containing characters godotenv
// would otherwise misread (spaces, '#', quotes) are double-quoted.
func envQuote(v string) string {
	v = strings.NewReplacer("\r", "", "\n", "").Replace(v)
	if strings.ContainsAny(v, " #\"'") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	return v
}

// dsn builds the driver connection string for mysql and postgres.
func (cfg dbConfig) dsn() string {
	switch cfg.Type {
	case "mysql":
		return cfg.User + ":" + cfg.Password + "@tcp(" + cfg.Host + ":" + cfg.Port + ")/" + cfg.Name + "?charset=utf8mb4&parseTime=True&loc=Local"
	case "postgres":
		// key=value form; single-quote values so spaces and '=' survive.
		q := func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'" }
		return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
			q(cfg.Host), q(cfg.Port), q(cfg.User), q(cfg.Password), q(cfg.Name), q(cfg.SSLMode))
	}
	return ""
}

// openDatabase opens the configured database. Used at startup and by the
// wizard's connection test so both agree on driver options.
func openDatabase(cfg dbConfig) (*gorm.DB, error) {
	switch cfg.Type {
	case "sqlite":
		return gorm.Open(sqlite.Open(cfg.SQLiteFile), &gorm.Config{
			DisableForeignKeyConstraintWhenMigrating: true,
		})
	case "mysql":
		return gorm.Open(mysql.Open(cfg.dsn()), &gorm.Config{})
	case "postgres":
		return gorm.Open(postgres.Open(cfg.dsn()), &gorm.Config{})
	}
	return nil, fmt.Errorf("database type %q is not valid, expecting sqlite, mysql or postgres", cfg.Type)
}
