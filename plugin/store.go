package plugin

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store is the persistent key/value storage a plugin gets through the
// store_* host functions. Keys are namespaced by plugin name.
type Store interface {
	Get(pluginName, key string) (value []byte, found bool, err error)
	Set(pluginName, key string, value []byte) error
	Delete(pluginName, key string) error
	List(pluginName, prefix string) ([]string, error)
	DeleteAll(pluginName string) error
}

const (
	MaxStoreKeyBytes   = 256
	MaxStoreValueBytes = 1 << 20
)

// ErrStoreUnavailable is returned before the database is configured (wizard).
var ErrStoreUnavailable = errors.New("plugin store: database not ready")

// PluginStoreEntry is one row of the plugin_store table.
type PluginStoreEntry struct {
	PluginName string `gorm:"primaryKey;size:64"`
	Key        string `gorm:"primaryKey;size:256"`
	Value      []byte
	UpdatedAt  time.Time
}

func (PluginStoreEntry) TableName() string { return "plugin_store" }

// Store returns the registry's database-backed Store. It reads the current
// db on every call, so it keeps working after the wizard sets one.
func (r *Registry) Store() Store { return &dbStore{r: r} }

type dbStore struct{ r *Registry }

func (s *dbStore) db() (*gorm.DB, error) {
	s.r.mu.RLock()
	defer s.r.mu.RUnlock()
	if s.r.db == nil {
		return nil, ErrStoreUnavailable
	}
	return s.r.db, nil
}

func (s *dbStore) Get(pluginName, key string) ([]byte, bool, error) {
	db, err := s.db()
	if err != nil {
		return nil, false, err
	}
	var e PluginStoreEntry
	err = db.Where("plugin_name = ? AND key = ?", pluginName, key).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return e.Value, true, nil
}

func (s *dbStore) Set(pluginName, key string, value []byte) error {
	if key == "" || len(key) > MaxStoreKeyBytes {
		return fmt.Errorf("plugin store: key must be 1-%d bytes", MaxStoreKeyBytes)
	}
	if len(value) > MaxStoreValueBytes {
		return fmt.Errorf("plugin store: value exceeds %d bytes", MaxStoreValueBytes)
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	e := PluginStoreEntry{PluginName: pluginName, Key: key, Value: value, UpdatedAt: time.Now()}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "plugin_name"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&e).Error
}

func (s *dbStore) Delete(pluginName, key string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Where("plugin_name = ? AND key = ?", pluginName, key).Delete(&PluginStoreEntry{}).Error
}

func (s *dbStore) List(pluginName, prefix string) ([]string, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var keys []string
	q := db.Model(&PluginStoreEntry{}).Where("plugin_name = ?", pluginName)
	if prefix != "" {
		q = q.Where("key LIKE ? ESCAPE '\\'", escapeLike(prefix)+"%")
	}
	if err := q.Order("key asc").Pluck("key", &keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func (s *dbStore) DeleteAll(pluginName string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Where("plugin_name = ?", pluginName).Delete(&PluginStoreEntry{}).Error
}

// escapeLike escapes LIKE wildcards so a prefix is matched literally.
func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' || s[i] == '_' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
