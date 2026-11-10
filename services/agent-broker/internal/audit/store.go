package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store is the local NDJSON audit file. Every Append prunes records whose own
// timestamp is older than Now()-Retention, so a long-running process never
// grows the file without bound (FR-52).
type Store struct {
	Path      string
	Retention time.Duration
	Now       func() time.Time

	mu sync.Mutex
}

// OpenStore creates the parent directories for path and returns a store. The
// file itself is created on the first Append.
func OpenStore(path string, retention time.Duration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Store{Path: path, Retention: retention, Now: time.Now}, nil
}

// Append prunes expired records and then writes one newline-terminated line.
func (s *Store) Append(r Record) error {
	if s == nil {
		return errors.New("audit: nil store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	now := s.Now
	if now == nil {
		now = time.Now
	}
	cutoff := now().Add(-s.Retention)

	existing, err := os.ReadFile(s.Path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var buf strings.Builder
	buf.Grow(len(existing) + len(line))
	for _, raw := range strings.Split(string(existing), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			// Keep a line we cannot parse rather than silently deleting an
			// audit record.
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}
		if rec.Time.Before(cutoff) {
			continue
		}
		buf.WriteString(raw)
		buf.WriteByte('\n')
	}
	buf.Write(line)

	return os.WriteFile(s.Path, []byte(buf.String()), 0o644)
}

// Close releases the store. The file is opened per Append, so this is a no-op
// that exists for callers' lifecycle symmetry.
func (s *Store) Close() error { return nil }
