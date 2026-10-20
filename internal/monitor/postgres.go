package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const databaseTimeout = 5 * time.Second

// The connection owns the session lock for the entire check. A storage error
// ends the check; reconnecting could overwrite a successfully committed state
// after a lost COMMIT response, or continue after losing exclusive ownership.
type postgresStore struct {
	conn *pgx.Conn
}

func databaseConfig() (*pgx.ConnConfig, error) {
	values := make(map[string]string)
	for _, key := range []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD"} {
		values[key] = os.Getenv(key)
		if strings.TrimSpace(values[key]) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return nil, errors.New("DB_PORT must be between 1 and 65535")
	}
	sslmode := os.Getenv("DB_SSLMODE")
	if sslmode == "" {
		sslmode = "require"
	}
	switch sslmode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return nil, errors.New("DB_SSLMODE must be disable, require, verify-ca or verify-full")
	}
	quote := func(value string) string {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
	}
	dsn := "host=" + quote(values["DB_HOST"]) + " port=" + port +
		" dbname=" + quote(values["DB_NAME"]) + " user=" + quote(values["DB_USER"]) +
		" password=" + quote(values["DB_PASSWORD"]) + " sslmode=" + sslmode +
		" connect_timeout=5 application_name=marketwatch"
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	return config, nil
}

func openState(parent context.Context, dryRun bool) (_ *postgresStore, _ State, result error) {
	config, err := databaseConfig()
	if err != nil {
		return nil, State{}, err
	}
	ctx, cancel := context.WithTimeout(parent, databaseTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, State{}, databaseError("connect", err)
	}
	store := &postgresStore{conn: conn}
	defer func() {
		if result != nil {
			store.close()
		}
	}()
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil {
		return nil, State{}, databaseError("read server version", err)
	}
	if version < 180000 {
		return nil, State{}, errors.New("PostgreSQL 18 or later is required")
	}
	if !dryRun {
		var owned bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended(current_database() || ':marketwatch', 0))`).Scan(&owned); err != nil {
			return nil, State{}, databaseError("acquire state lock", err)
		}
		if !owned {
			return nil, State{}, errors.New("another Marketwatch check owns the PostgreSQL state lock")
		}
	}
	opts := pgx.TxOptions{}
	if dryRun {
		opts = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, State{}, databaseError("begin state read", err)
	}
	defer tx.Rollback(ctx)
	var table *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.marketwatch_state')::text`).Scan(&table); err != nil {
		return nil, State{}, databaseError("find state table", err)
	}
	state := emptyState()
	if table == nil && !dryRun {
		data, err := json.Marshal(state)
		if err != nil {
			return nil, State{}, fmt.Errorf("encode initial state: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE public.marketwatch_state (
			id boolean PRIMARY KEY DEFAULT true CHECK (id), state jsonb NOT NULL
		)`); err != nil {
			return nil, State{}, databaseError("create state table", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO public.marketwatch_state (id, state) VALUES (true, $1)`, data); err != nil {
			return nil, State{}, databaseError("initialize state", err)
		}
	} else if table != nil {
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT state FROM public.marketwatch_state WHERE id = true`).Scan(&data); err != nil {
			return nil, State{}, databaseError("read state", err)
		}
		state, err = decodeState(data)
		if err != nil {
			return nil, State{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, State{}, databaseError("commit state read", err)
	}
	return store, state, nil
}

func (s *postgresStore) save(parent context.Context, state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if _, err := decodeState(data); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, databaseTimeout)
	defer cancel()
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return databaseError("begin state save", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE public.marketwatch_state SET state = $1 WHERE id = true`, data)
	if err != nil {
		return databaseError("save state", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("PostgreSQL state singleton is missing; state was not reset")
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("commit state save", err)
	}
	return nil
}

func (s *postgresStore) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.conn.Close(ctx)
}

// Driver errors can contain connection strings, passwords, or data from SQL.
// Surface only the operation and an optional SQLSTATE, never the raw error.
func databaseError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("PostgreSQL state singleton is missing; state was not reset")
	}
	var serverError *pgconn.PgError
	if errors.As(err, &serverError) {
		return fmt.Errorf("PostgreSQL %s failed (SQLSTATE %s)", operation, serverError.Code)
	}
	return fmt.Errorf("PostgreSQL %s failed or cancelled", operation)
}
