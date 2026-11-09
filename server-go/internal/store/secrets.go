package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
)

// Stored secrets that are encrypted with KUMA_SECRET_KEY: authenticator keys, GitHub tokens,
// notification settings, proxy passwords and the VAPID private key. Each is sealed for its column
// and row, so a value copied to another row doesn't open.
//
// Monitors' credentials are not: they're spread over many columns that checks, the API and the UI
// read, and the Grafana role can't read them already.

// SetKeyring sets the key that encrypts stored secrets. A nil keyring stores them as they are.
func (s *Store) SetKeyring(k *secrets.Keyring) { s.keys = k }

func secretAAD(table, column string, row any) string {
	return fmt.Sprintf("%s.%s:%v", table, column, row)
}

func (s *Store) sealText(plain, aad string) (string, error) {
	return s.keys.Seal([]byte(plain), aad)
}

func (s *Store) openText(stored, aad string) (string, error) {
	plain, err := s.keys.Open(stored, aad)
	return string(plain), err
}

// sealJSON returns the value to store in a jsonb column: v itself without a key, or the sealed
// text of its JSON as a JSON string.
func (s *Store) sealJSON(v any, aad string) (any, error) {
	if !s.keys.Enabled() {
		return v, nil
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sealed, err := s.keys.Seal(plain, aad)
	if err != nil {
		return nil, err
	}
	quoted, _ := json.Marshal(sealed)
	return json.RawMessage(quoted), nil
}

// openJSON reads a jsonb column that sealJSON wrote, into v.
func (s *Store) openJSON(raw []byte, aad string, v any) error {
	var text string
	if json.Unmarshal(raw, &text) == nil && secrets.IsSealed(text) {
		plain, err := s.keys.Open(text, aad)
		if err != nil {
			return err
		}
		raw = plain
	}
	return json.Unmarshal(raw, v)
}

// openUser decrypts an account's authenticator key.
func (s *Store) openUser(u *User) error {
	if u.TOTPSecret == nil || !secrets.IsSealed(string(u.TOTPSecret)) {
		return nil
	}
	plain, err := s.keys.Open(string(u.TOTPSecret), secretAAD("app_user", "totp_secret", u.ID))
	if err != nil {
		return err
	}
	u.TOTPSecret = plain
	return nil
}

// sealTOTP is what's stored for an authenticator key.
func (s *Store) sealTOTP(userID int32, secret []byte) ([]byte, error) {
	if secret == nil || !s.keys.Enabled() {
		return secret, nil
	}
	sealed, err := s.keys.Seal(secret, secretAAD("app_user", "totp_secret", userID))
	return []byte(sealed), err
}

// secretColumn is a place where secrets are stored, for SealSecrets.
type secretColumn struct {
	name string
	// list returns each stored value's row and its text, which is sealed or plain.
	list string
	// update stores a new value for row $1, if the value is still $3. $2 is the new value.
	update string
	// jsonString means that the value is a jsonb string, and json that it's any jsonb.
	jsonString, json bool
	bytea            bool
	table, column    string
}

var secretColumns = []secretColumn{
	{
		name: "authenticator keys", table: "app_user", column: "totp_secret", bytea: true,
		list:   `SELECT id::text, totp_secret FROM app_user WHERE totp_secret IS NOT NULL`,
		update: `UPDATE app_user SET totp_secret = $2 WHERE id = $1::int AND totp_secret = $3`,
	},
	{
		name: "GitHub tokens", table: "app_user", column: "github_token",
		list:   `SELECT id::text, github_token FROM app_user WHERE github_token IS NOT NULL AND github_token <> ''`,
		update: `UPDATE app_user SET github_token = $2 WHERE id = $1::int AND github_token = $3`,
	},
	{
		name: "notification settings", table: "notification", column: "config", json: true,
		list:   `SELECT id::text, config::text FROM notification`,
		update: `UPDATE notification SET config = $2::jsonb WHERE id = $1::int AND config = $3::jsonb`,
	},
	{
		name: "proxy passwords", table: "proxy", column: "password",
		list:   `SELECT id::text, password FROM proxy WHERE password <> ''`,
		update: `UPDATE proxy SET password = $2 WHERE id = $1::int AND password = $3`,
	},
	{
		name: "status page webhooks", table: "status_page_subscriber", column: "address",
		list:   `SELECT id::text, address FROM status_page_subscriber WHERE kind = 'webhook'`,
		update: `UPDATE status_page_subscriber SET address = $2 WHERE id = $1::int AND address = $3`,
	},
	{
		name: "the GitHub client secret", table: "login_config", column: "github_client_secret",
		list:   `SELECT id::text, github_client_secret FROM login_config WHERE github_client_secret <> ''`,
		update: `UPDATE login_config SET github_client_secret = $2 WHERE id = $1::int AND github_client_secret = $3`,
	},
	{
		name: "the VAPID private key", table: "app_setting", column: "value", jsonString: true,
		list:   `SELECT key, value::text FROM app_setting WHERE key = '` + WebpushPrivateKeySetting + `'`,
		update: `UPDATE app_setting SET value = $2::jsonb WHERE key = $1 AND value = $3::jsonb`,
	},
}

// SecretsReport counts what SealSecrets did.
type SecretsReport struct {
	// Sealed were plain, and are now encrypted.
	Sealed map[string]int
	// Resealed were encrypted with an older key, and are now with the current one.
	Resealed map[string]int
}

// SealSecrets checks every stored secret at start. It encrypts plain ones when there's a key, and
// with rekey, encrypts again those sealed with an older key. It fails, so that the server doesn't
// start, when a value is encrypted and there's no key, or with a key that isn't in the keyring.
func (s *Store) SealSecrets(ctx context.Context, rekey bool) (SecretsReport, error) {
	report := SecretsReport{Sealed: map[string]int{}, Resealed: map[string]int{}}
	for _, col := range secretColumns {
		rows, err := s.pool.Query(ctx, col.list)
		if err != nil {
			return report, fmt.Errorf("read %s: %w", col.name, err)
		}
		type stored struct {
			row   string
			value []byte
		}
		values, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
			var v stored
			err := r.Scan(&v.row, &v.value)
			return v, err
		})
		if err != nil {
			return report, fmt.Errorf("read %s: %w", col.name, err)
		}
		for _, v := range values {
			text := string(v.value)
			if col.json || col.jsonString {
				var str string
				if json.Unmarshal(v.value, &str) == nil && (col.jsonString || secrets.IsSealed(str)) {
					text = str
				}
			}
			aad := secretAAD(col.table, col.column, v.row)
			var plain []byte
			switch {
			case secrets.IsSealed(text):
				opened, err := s.keys.Open(text, aad)
				if err != nil {
					return report, fmt.Errorf("%s, row %s: %w", col.name, v.row, err)
				}
				if !rekey || secrets.KeyID(text) == s.keys.CurrentID() {
					continue
				}
				plain = opened
			case !s.keys.Enabled():
				continue
			default:
				plain = []byte(text)
			}
			sealed, err := s.keys.Seal(plain, aad)
			if err != nil {
				return report, err
			}
			var newValue, oldValue any = sealed, text
			switch {
			case col.bytea:
				newValue, oldValue = []byte(sealed), v.value
			case col.json || col.jsonString:
				quoted, _ := json.Marshal(sealed)
				newValue, oldValue = string(quoted), string(v.value)
			}
			if _, err := s.pool.Exec(ctx, col.update, v.row, newValue, oldValue); err != nil {
				return report, fmt.Errorf("encrypt %s, row %s: %w", col.name, v.row, err)
			}
			if secrets.IsSealed(text) {
				report.Resealed[col.name]++
			} else {
				report.Sealed[col.name]++
			}
		}
	}
	return report, nil
}
