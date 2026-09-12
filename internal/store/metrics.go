package store

import (
	"context"
	"time"
)

// Metrics is a fleet-wide summary for the /metrics endpoint.
type Metrics struct {
	Hosts            []Host
	DrivesByStatus   map[string]int
	KernelWarnings24 int // kernel_warning events in the last 24 hours
	Events24         int // all events in the last 24 hours
}

// Metrics gathers the summary. It is cheap enough to run on every scrape.
func (s *Store) Metrics(ctx context.Context) (Metrics, error) {
	m := Metrics{DrivesByStatus: map[string]int{}}
	hosts, err := s.ListHosts(ctx)
	if err != nil {
		return m, err
	}
	m.Hosts = hosts
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM drive WHERE merged_into IS NULL GROUP BY status`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return m, err
		}
		m.DrivesByStatus[status] = n
	}
	if err := rows.Err(); err != nil {
		return m, err
	}
	since := s.now().Add(-24 * time.Hour).Unix()
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE kind = ? AND ts >= ?`, EventKernelWarning, since).Scan(&m.KernelWarnings24); err != nil {
		return m, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event WHERE ts >= ?`, since).Scan(&m.Events24); err != nil {
		return m, err
	}
	return m, nil
}
