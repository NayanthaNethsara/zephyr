package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/migrations"
)

func TestPluralRenameKeepsExistingRows(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{name: "pending entry survives the upgrade", state: "pending"},
		{name: "failed entry survives the upgrade", state: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "gateway.db")

			legacy, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			provider, err := goose.NewProvider(goose.DialectSQLite3, legacy, migrations.FS)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.UpTo(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.ExecContext(ctx, `INSERT INTO dedup (dedup_key, expires_at) VALUES ('msg:1', 1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.ExecContext(ctx,
				`INSERT INTO outbox (id, topic, msg_key, payload, next_attempt_at, state, created_at, updated_at)
				 VALUES ('evt_1', 'wa.inbound', 'k', x'7b7d', 1, ?, 1, 1)`, tt.state); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			s, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open() on legacy database error = %v", err)
			}
			defer func() { _ = s.Close() }()

			if got := countRows(t, s, `SELECT COUNT(*) FROM dedup_keys`); got != 1 {
				t.Errorf("dedup_keys rows = %d, want 1", got)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM outbox_entries WHERE state = '`+tt.state+`'`); got != 1 {
				t.Errorf("%s outbox_entries rows = %d, want 1", tt.state, got)
			}
		})
	}
}
