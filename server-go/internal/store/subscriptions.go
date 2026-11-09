package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kinds of status page subscriber.
const (
	SubscriberEmail   = "email"
	SubscriberWebhook = "webhook"
)

// ErrSubscriberExists means that the page already sends to the address.
var ErrSubscriberExists = errors.New("this address is already subscribed")

// Subscriber gets a status page's incidents by email or webhook. An email subscriber gets them
// once confirmed; a webhook, which an admin adds, is confirmed from the start.
type Subscriber struct {
	ID           int32
	StatusPageID int32
	Kind         string
	// Address is the email address or the webhook's URL.
	Address       string
	Token         string
	Confirmed     bool
	CreatedAt     time.Time
	ConfirmSentAt *time.Time
	LastSentAt    *time.Time
	LastError     string
}

// IncidentTemplate is a ready incident to start from.
type IncidentTemplate struct {
	ID      int32
	Title   string
	Content string
	Style   string
}

const subscriberColumns = `id, status_page_id, kind, address, token, confirmed, created_at, confirm_sent_at, last_sent_at, last_error`

func subscriberAAD(id int32) string { return secretAAD("status_page_subscriber", "address", id) }

// addressKey is what keeps one subscription per address: emails ignore case, and a webhook's URL,
// which is sealed, is compared by its hash.
func addressKey(kind, address string) string {
	if kind == SubscriberEmail {
		return strings.ToLower(address)
	}
	sum := sha256.Sum256([]byte(address))
	return hex.EncodeToString(sum[:])
}

func (s *Store) collectSubscribers(rows pgx.Rows) ([]Subscriber, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Subscriber, error) {
		var sub Subscriber
		err := r.Scan(&sub.ID, &sub.StatusPageID, &sub.Kind, &sub.Address, &sub.Token, &sub.Confirmed, &sub.CreatedAt,
			&sub.ConfirmSentAt, &sub.LastSentAt, &sub.LastError)
		if err != nil {
			return sub, err
		}
		if sub.Kind == SubscriberWebhook {
			if sub.Address, err = s.openText(sub.Address, subscriberAAD(sub.ID)); err != nil {
				return sub, fmt.Errorf("subscriber %d: %w", sub.ID, err)
			}
		}
		return sub, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read subscribers: %w", err)
	}
	return list, nil
}

func (s *Store) oneSubscriber(ctx context.Context, query string, args ...any) (Subscriber, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Subscriber{}, fmt.Errorf("subscriber query: %w", err)
	}
	list, err := s.collectSubscribers(rows)
	if err != nil {
		return Subscriber{}, err
	}
	if len(list) == 0 {
		return Subscriber{}, ErrNotFound
	}
	return list[0], nil
}

// Subscribers returns a page's subscribers, emails first, oldest first.
func (s *Store) Subscribers(ctx context.Context, pageID int32) ([]Subscriber, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+subscriberColumns+` FROM status_page_subscriber
		WHERE status_page_id = $1 ORDER BY kind, created_at, id`, pageID)
	if err != nil {
		return nil, fmt.Errorf("list subscribers: %w", err)
	}
	return s.collectSubscribers(rows)
}

// CountSubscribers counts a page's subscribers, confirmed or not.
func (s *Store) CountSubscribers(ctx context.Context, pageID int32) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM status_page_subscriber WHERE status_page_id = $1`, pageID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count subscribers: %w", err)
	}
	return n, nil
}

// AddEmailSubscriber adds an unconfirmed email subscriber with the token, or returns the page's
// subscriber with that address, confirmed or not, and whether it was added.
func (s *Store) AddEmailSubscriber(ctx context.Context, pageID int32, email, token string) (Subscriber, bool, error) {
	key := addressKey(SubscriberEmail, email)
	sub, err := s.oneSubscriber(ctx, `INSERT INTO status_page_subscriber (status_page_id, kind, address, address_key, token)
		VALUES ($1, 'email', $2, $3, $4) ON CONFLICT (status_page_id, kind, address_key) DO NOTHING
		RETURNING `+subscriberColumns, pageID, email, key, token)
	if err == nil {
		return sub, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return sub, false, err
	}
	sub, err = s.oneSubscriber(ctx, `SELECT `+subscriberColumns+` FROM status_page_subscriber
		WHERE status_page_id = $1 AND kind = 'email' AND address_key = $2`, pageID, key)
	return sub, false, err
}

// AddWebhookSubscriber adds a confirmed webhook, or returns ErrSubscriberExists.
func (s *Store) AddWebhookSubscriber(ctx context.Context, pageID int32, url, token string) (Subscriber, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Subscriber{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id int32
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('status_page_subscriber', 'id'))::int`).Scan(&id); err != nil {
		return Subscriber{}, fmt.Errorf("add webhook: %w", err)
	}
	sealed, err := s.sealText(url, subscriberAAD(id))
	if err != nil {
		return Subscriber{}, err
	}
	var created time.Time
	err = tx.QueryRow(ctx, `INSERT INTO status_page_subscriber (id, status_page_id, kind, address, address_key, token, confirmed)
		VALUES ($1, $2, 'webhook', $3, $4, $5, true) RETURNING created_at`,
		id, pageID, sealed, addressKey(SubscriberWebhook, url), token).Scan(&created)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return Subscriber{}, ErrSubscriberExists
	}
	if err != nil {
		return Subscriber{}, fmt.Errorf("add webhook: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Subscriber{}, fmt.Errorf("add webhook: %w", err)
	}
	return Subscriber{ID: id, StatusPageID: pageID, Kind: SubscriberWebhook, Address: url, Token: token, Confirmed: true, CreatedAt: created}, nil
}

// ClaimConfirmation marks that a confirmation email is going to an unconfirmed subscriber, unless
// one went within the interval. It reports whether to send it.
func (s *Store) ClaimConfirmation(ctx context.Context, id int32, interval time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE status_page_subscriber SET confirm_sent_at = now()
		WHERE id = $1 AND NOT confirmed AND (confirm_sent_at IS NULL OR confirm_sent_at < now() - $2::interval)`,
		id, interval.String())
	if err != nil {
		return false, fmt.Errorf("claim confirmation: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ConfirmSubscriber confirms the page's subscriber with the token, or returns ErrNotFound.
func (s *Store) ConfirmSubscriber(ctx context.Context, pageID int32, token string) (Subscriber, error) {
	return s.oneSubscriber(ctx, `UPDATE status_page_subscriber SET confirmed = true
		WHERE status_page_id = $1 AND token = $2 RETURNING `+subscriberColumns, pageID, token)
}

// RemoveSubscriberByToken removes the page's subscriber with the token, or returns ErrNotFound.
func (s *Store) RemoveSubscriberByToken(ctx context.Context, pageID int32, token string) (Subscriber, error) {
	return s.oneSubscriber(ctx, `DELETE FROM status_page_subscriber WHERE status_page_id = $1 AND token = $2
		RETURNING `+subscriberColumns, pageID, token)
}

// RemoveSubscriber removes one of a page's subscribers, or returns ErrNotFound.
func (s *Store) RemoveSubscriber(ctx context.Context, pageID, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM status_page_subscriber WHERE status_page_id = $1 AND id = $2`, pageID, id)
	if err != nil {
		return fmt.Errorf("remove subscriber: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUnconfirmedSubscribers removes email subscribers that weren't confirmed before the time.
func (s *Store) DeleteUnconfirmedSubscribers(ctx context.Context, before time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM status_page_subscriber WHERE NOT confirmed AND created_at < $1`, before); err != nil {
		return fmt.Errorf("delete unconfirmed subscribers: %w", err)
	}
	return nil
}

// RecordDelivery keeps when a subscriber was last sent to, and the error, or "" if it worked.
func (s *Store) RecordDelivery(ctx context.Context, id int32, sendErr string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE status_page_subscriber SET last_sent_at = now(), last_error = $2 WHERE id = $1`,
		id, sendErr); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

// SetStatusPageSubscriptions sets the notification that sends a page's emails, or nil for none.
func (s *Store) SetStatusPageSubscriptions(ctx context.Context, pageID int32, notificationID *int32) error {
	tag, err := s.pool.Exec(ctx, `UPDATE status_page SET subscribe_notification_id = $2, updated_at = now() WHERE id = $1`,
		pageID, notificationID)
	if err != nil {
		return fmt.Errorf("save subscriptions: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IncidentTemplates returns every template, by title.
func (s *Store) IncidentTemplates(ctx context.Context) ([]IncidentTemplate, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, title, content, style FROM incident_template ORDER BY lower(title), id`)
	if err != nil {
		return nil, fmt.Errorf("list incident templates: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[IncidentTemplate])
}

// SaveIncidentTemplate saves a template, replacing the one with its title.
func (s *Store) SaveIncidentTemplate(ctx context.Context, t IncidentTemplate) (IncidentTemplate, error) {
	err := s.pool.QueryRow(ctx, `INSERT INTO incident_template (title, content, style) VALUES ($1, $2, $3)
		ON CONFLICT (title) DO UPDATE SET content = EXCLUDED.content, style = EXCLUDED.style, updated_at = now()
		RETURNING id`, t.Title, t.Content, t.Style).Scan(&t.ID)
	if err != nil {
		return t, fmt.Errorf("save incident template: %w", err)
	}
	return t, nil
}

// CountIncidentTemplates counts the templates.
func (s *Store) CountIncidentTemplates(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM incident_template`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count incident templates: %w", err)
	}
	return n, nil
}

// DeleteIncidentTemplate removes a template, or returns ErrNotFound.
func (s *Store) DeleteIncidentTemplate(ctx context.Context, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM incident_template WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete incident template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
