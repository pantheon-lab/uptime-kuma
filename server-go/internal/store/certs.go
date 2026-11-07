package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TLSFingerprint returns the fingerprint of a monitor's stored certificate, or "" if there's none.
func (s *Store) TLSFingerprint(ctx context.Context, monitorID int32) (string, error) {
	var fingerprint string
	err := s.pool.QueryRow(ctx, `SELECT fingerprint FROM monitor_tls_info WHERE monitor_id = $1`, monitorID).Scan(&fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read certificate: %w", err)
	}
	return fingerprint, nil
}

// SaveTLSInfo stores a monitor's certificate. A monitor that was deleted meanwhile is ignored.
func (s *Store) SaveTLSInfo(ctx context.Context, monitorID int32, info []byte, fingerprint string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO monitor_tls_info (monitor_id, info, fingerprint)
		SELECT id, $2::text, $3::text FROM monitor WHERE id = $1
		ON CONFLICT (monitor_id) DO UPDATE SET info = excluded.info, fingerprint = excluded.fingerprint, updated_at = now()`,
		monitorID, string(info), fingerprint)
	if err != nil {
		return fmt.Errorf("save certificate: %w", err)
	}
	return nil
}

// AllTLSInfo returns every monitor's stored certificate information, as JSON.
func (s *Store) AllTLSInfo(ctx context.Context) (map[int32]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT monitor_id, info FROM monitor_tls_info`)
	if err != nil {
		return nil, fmt.Errorf("query certificates: %w", err)
	}
	out := map[int32]string{}
	var id int32
	var info string
	if _, err := pgx.ForEachRow(rows, []any{&id, &info}, func() error {
		out[id] = info
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read certificates: %w", err)
	}
	return out, nil
}

// ClearCertAlerts forgets which expiry alerts were sent for a monitor.
func (s *Store) ClearCertAlerts(ctx context.Context, monitorID int32) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM cert_alert_sent WHERE monitor_id = $1`, monitorID); err != nil {
		return fmt.Errorf("clear certificate alerts: %w", err)
	}
	return nil
}

// CertAlertSent reports whether an alert was sent at this deadline or a closer one.
func (s *Store) CertAlertSent(ctx context.Context, monitorID int32, days int) (bool, error) {
	var sent bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cert_alert_sent WHERE monitor_id = $1 AND days <= $2)`,
		monitorID, days).Scan(&sent)
	if err != nil {
		return false, fmt.Errorf("read certificate alerts: %w", err)
	}
	return sent, nil
}

// RecordCertAlert remembers an alert sent at a deadline. A monitor deleted meanwhile is ignored.
func (s *Store) RecordCertAlert(ctx context.Context, monitorID int32, days int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cert_alert_sent (monitor_id, days) SELECT id, $2::int FROM monitor WHERE id = $1 ON CONFLICT DO NOTHING`,
		monitorID, days)
	if err != nil {
		return fmt.Errorf("record certificate alert: %w", err)
	}
	return nil
}
