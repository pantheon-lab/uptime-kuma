package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
)

const maintenanceColumns = `id, title, description, strategy, active, start_date, end_date, start_time, end_time,
	weekdays, days_of_month, interval_day, cron, duration_minutes, timezone, holiday_mode, holiday_countries`

func scanMaintenance(row pgx.Row) (maintenance.Window, error) {
	var w maintenance.Window
	err := row.Scan(&w.ID, &w.Title, &w.Description, &w.Strategy, &w.Active, &w.StartDate, &w.EndDate,
		&w.StartTime, &w.EndTime, &w.Weekdays, &w.DaysOfMonth, &w.IntervalDay, &w.Cron, &w.DurationMinutes,
		&w.Timezone, &w.HolidayMode, &w.HolidayCountries)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, fmt.Errorf("read maintenance: %w", err)
	}
	return w, nil
}

func maintenanceArgs(w maintenance.Window) []any {
	weekdays, days, countries := w.Weekdays, w.DaysOfMonth, w.HolidayCountries
	if weekdays == nil {
		weekdays = []int{}
	}
	if days == nil {
		days = []int{}
	}
	if countries == nil {
		countries = []string{}
	}
	return []any{w.Title, w.Description, w.Strategy, w.Active, w.StartDate, w.EndDate, w.StartTime, w.EndTime,
		weekdays, days, w.IntervalDay, w.Cron, w.DurationMinutes, w.Timezone, w.HolidayMode, countries}
}

// ListMaintenances returns every maintenance window.
func (s *Store) ListMaintenances(ctx context.Context) ([]maintenance.Window, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+maintenanceColumns+` FROM maintenance ORDER BY end_date DESC, title`)
	if err != nil {
		return nil, fmt.Errorf("query maintenance: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (maintenance.Window, error) { return scanMaintenance(r) })
}

// GetMaintenance returns a window or ErrNotFound.
func (s *Store) GetMaintenance(ctx context.Context, id int32) (maintenance.Window, error) {
	return scanMaintenance(s.pool.QueryRow(ctx, `SELECT `+maintenanceColumns+` FROM maintenance WHERE id = $1`, id))
}

// CreateMaintenance stores a new window.
func (s *Store) CreateMaintenance(ctx context.Context, w maintenance.Window) (maintenance.Window, error) {
	return scanMaintenance(s.pool.QueryRow(ctx, `
		INSERT INTO maintenance (title, description, strategy, active, start_date, end_date, start_time, end_time,
			weekdays, days_of_month, interval_day, cron, duration_minutes, timezone, holiday_mode, holiday_countries)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING `+maintenanceColumns, maintenanceArgs(w)...))
}

// UpdateMaintenance replaces a window's rules, or returns ErrNotFound.
func (s *Store) UpdateMaintenance(ctx context.Context, w maintenance.Window) (maintenance.Window, error) {
	args := append(maintenanceArgs(w), w.ID)
	return scanMaintenance(s.pool.QueryRow(ctx, `
		UPDATE maintenance SET title = $1, description = $2, strategy = $3, active = $4, start_date = $5,
			end_date = $6, start_time = $7, end_time = $8, weekdays = $9, days_of_month = $10, interval_day = $11,
			cron = $12, duration_minutes = $13, timezone = $14, holiday_mode = $15, holiday_countries = $16
		WHERE id = $17
		RETURNING `+maintenanceColumns, args...))
}

// SetMaintenanceActive pauses or resumes a window, or returns ErrNotFound.
func (s *Store) SetMaintenanceActive(ctx context.Context, id int32, active bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE maintenance SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("set maintenance active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMaintenance deletes a window and its links, or returns ErrNotFound.
func (s *Store) DeleteMaintenance(ctx context.Context, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM maintenance WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete maintenance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMaintenanceMonitors replaces the monitors a window covers.
func (s *Store) SetMaintenanceMonitors(ctx context.Context, maintenanceID int32, monitorIDs []int32) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance WHERE id = $1)`, maintenanceID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM monitor_maintenance WHERE maintenance_id = $1`, maintenanceID); err != nil {
			return err
		}
		if len(monitorIDs) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO monitor_maintenance (monitor_id, maintenance_id)
			SELECT DISTINCT unnest($2::int[]), $1::int ON CONFLICT DO NOTHING`, maintenanceID, monitorIDs)
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, ErrNotFound):
		return err
	case errors.As(err, &pgErr) && pgErr.Code == "23503":
		return fmt.Errorf("%w: a monitor doesn't exist", ErrNotFound)
	case err != nil:
		return fmt.Errorf("set maintenance monitors: %w", err)
	}
	return nil
}

// MaintenanceMonitorLinks returns the monitors linked to each window.
func (s *Store) MaintenanceMonitorLinks(ctx context.Context) (map[int32][]int32, error) {
	rows, err := s.pool.Query(ctx, `SELECT maintenance_id, monitor_id FROM monitor_maintenance ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("query maintenance monitors: %w", err)
	}
	links := map[int32][]int32{}
	var maintenanceID, monitorID int32
	_, err = pgx.ForEachRow(rows, []any{&maintenanceID, &monitorID}, func() error {
		links[maintenanceID] = append(links[maintenanceID], monitorID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read maintenance monitors: %w", err)
	}
	return links, nil
}

// HolidayOverrides returns admins' changes to the holiday calendars.
func (s *Store) HolidayOverrides(ctx context.Context) ([]holidays.Override, error) {
	rows, err := s.pool.Query(ctx, `SELECT country, to_char(date, 'YYYY-MM-DD'), name, removed FROM holiday_override`)
	if err != nil {
		return nil, fmt.Errorf("query holiday overrides: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (holidays.Override, error) {
		var o holidays.Override
		err := r.Scan(&o.Country, &o.Date, &o.Name, &o.Removed)
		return o, err
	})
}

// SetHolidayOverride adds a date to a country's calendar, or marks a bundled one as removed.
func (s *Store) SetHolidayOverride(ctx context.Context, o holidays.Override) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO holiday_override (country, date, name, removed) VALUES ($1, $2::date, $3, $4)
		ON CONFLICT (country, date) DO UPDATE SET name = excluded.name, removed = excluded.removed`,
		strings.ToUpper(o.Country), o.Date, o.Name, o.Removed)
	if err != nil {
		return fmt.Errorf("set holiday override: %w", err)
	}
	return nil
}

// DeleteHolidayOverride undoes a change: a custom date goes, a removed bundled date comes back.
func (s *Store) DeleteHolidayOverride(ctx context.Context, country, date string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM holiday_override WHERE country = $1 AND date = $2::date`,
		strings.ToUpper(country), date); err != nil {
		return fmt.Errorf("delete holiday override: %w", err)
	}
	return nil
}
