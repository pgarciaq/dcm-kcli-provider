// Package store provides a bbolt-backed persistent key-value store for resource state.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrNotFound   = errors.New("resource not found")
	bucketName    = []byte("resources")
	nameIdxBucket = []byte("name_index")
	metaBucket    = []byte("meta")
	versionKey    = []byte("schema_version")
)

const currentSchemaVersion = 1

type MigrationFunc func(tx *bolt.Tx) error

var migrations = []MigrationFunc{
	// Index 0 is version 0 -> 1 (initial schema, no-op since New() creates the buckets)
}

type ResourceEntry struct {
	ID        string    `json:"id"`
	KcliName  string    `json:"kcli_name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	db *bolt.DB
}

func New(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening bbolt store: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketName); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(nameIdxBucket); err != nil {
			return err
		}
		mb, err := tx.CreateBucketIfNotExists(metaBucket)
		if err != nil {
			return err
		}
		return runMigrations(tx, mb)
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initializing store: %w", err)
	}

	return &Store{db: db}, nil
}

func SchemaVersion(db *bolt.DB) (int, error) {
	var version int
	err := db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(metaBucket)
		if mb == nil {
			return nil
		}
		v := mb.Get(versionKey)
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &version)
	})
	return version, err
}

func runMigrations(tx *bolt.Tx, mb *bolt.Bucket) error {
	var storedVersion int
	v := mb.Get(versionKey)
	if v != nil {
		if err := json.Unmarshal(v, &storedVersion); err != nil {
			return fmt.Errorf("reading schema version: %w", err)
		}
	}

	for i := storedVersion; i < currentSchemaVersion; i++ {
		if i < len(migrations) {
			if err := migrations[i](tx); err != nil {
				return fmt.Errorf("migration %d -> %d failed: %w", i, i+1, err)
			}
		}
	}

	data, err := json.Marshal(currentSchemaVersion)
	if err != nil {
		return err
	}
	return mb.Put(versionKey, data)
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *bolt.DB {
	return s.db
}

func (s *Store) Put(entry ResourceEntry) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketName).Put([]byte(entry.ID), data); err != nil {
			return err
		}
		return tx.Bucket(nameIdxBucket).Put([]byte(entry.KcliName), []byte(entry.ID))
	})
}

func (s *Store) Get(id string) (*ResourceEntry, error) {
	var entry ResourceEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketName).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &entry)
	})
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func (s *Store) List(resourceType string) ([]ResourceEntry, error) {
	var entries []ResourceEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).ForEach(func(_, v []byte) error {
			var entry ResourceEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return err
			}
			if entry.Type == resourceType {
				entries = append(entries, entry)
			}
			return nil
		})
	})
	return entries, err
}

func (s *Store) ListByStatus(resourceType, status string) ([]ResourceEntry, error) {
	var entries []ResourceEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).ForEach(func(_, v []byte) error {
			var entry ResourceEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return err
			}
			if entry.Type == resourceType && entry.Status == status {
				entries = append(entries, entry)
			}
			return nil
		})
	})
	return entries, err
}

func (s *Store) Delete(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketName).Get([]byte(id))
		if data != nil {
			var entry ResourceEntry
			if err := json.Unmarshal(data, &entry); err == nil {
				_ = tx.Bucket(nameIdxBucket).Delete([]byte(entry.KcliName))
			}
		}
		return tx.Bucket(bucketName).Delete([]byte(id))
	})
}

func (s *Store) UpdateStatus(id, newStatus string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketName).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		var entry ResourceEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return err
		}
		entry.Status = newStatus
		updated, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketName).Put([]byte(id), updated)
	})
}

func (s *Store) ResolveKcliName(dcmID string) (string, error) {
	entry, err := s.Get(dcmID)
	if err != nil {
		return "", err
	}
	return entry.KcliName, nil
}

func (s *Store) FindByKcliName(name string) (*ResourceEntry, error) {
	var entry *ResourceEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		idBytes := tx.Bucket(nameIdxBucket).Get([]byte(name))
		if idBytes == nil {
			return ErrNotFound
		}
		data := tx.Bucket(bucketName).Get(idBytes)
		if data == nil {
			return ErrNotFound
		}
		entry = &ResourceEntry{}
		return json.Unmarshal(data, entry)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *Store) ListAll() ([]ResourceEntry, error) {
	var entries []ResourceEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).ForEach(func(_, v []byte) error {
			var entry ResourceEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return err
			}
			entries = append(entries, entry)
			return nil
		})
	})
	return entries, err
}
