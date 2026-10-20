package monitor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const testStateTable = "public.marketwatch_state"

func postgresTestURL(t *testing.T, applicationName string) string {
	t.Helper()
	if os.Getenv("MARKETWATCH_TEST_POSTGRES") != "1" {
		t.Fatal("E2E tests require MARKETWATCH_TEST_POSTGRES=1 and a disposable database")
	}
	if os.Getenv("DB_NAME") != "marketwatch_test" {
		t.Fatal("E2E tests require DB_NAME=marketwatch_test")
	}
	host := os.Getenv("DB_HOST")
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatal("E2E tests require DB_HOST to be a loopback IP address")
	}
	user, password := os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")
	if user == "" || password == "" {
		t.Fatal("E2E tests require nonempty DB_USER and DB_PASSWORD")
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		t.Fatal("E2E tests require DB_PORT to be a valid TCP port")
	}
	sslmode := os.Getenv("DB_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}
	if sslmode != "disable" {
		t.Fatal("E2E tests require DB_SSLMODE=disable for the loopback fixture")
	}

	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/marketwatch_test",
	}
	query := u.Query()
	query.Set("sslmode", sslmode)
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	return u.String()
}

func connectPostgresTest(t *testing.T, applicationName string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, postgresTestURL(t, applicationName))
	if err != nil {
		t.Fatal("connect to disposable PostgreSQL fixture failed")
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := conn.Close(closeCtx); err != nil {
			t.Errorf("close PostgreSQL fixture connection: %v", err)
		}
	})
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil {
		t.Fatalf("read disposable PostgreSQL fixture version: %v", err)
	}
	if version < 180000 {
		t.Fatal("E2E tests require PostgreSQL 18 or later")
	}
	return conn
}

func resetPostgresTestState(t *testing.T) {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	if _, err := conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+testStateTable); err != nil {
		t.Fatalf("reset disposable PostgreSQL state table: %v", err)
	}
}

func createEmptyPostgresTestState(t *testing.T) {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	_, err := conn.Exec(context.Background(), `
		CREATE TABLE public.marketwatch_state (
			id boolean PRIMARY KEY DEFAULT true CHECK (id),
			state jsonb NOT NULL
		)`)
	if err != nil {
		t.Fatalf("create empty test state table: %v", err)
	}
}

func postgresTestStateJSON(t *testing.T) ([]byte, bool) {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	var table *string
	if err := conn.QueryRow(context.Background(), "SELECT to_regclass($1)", testStateTable).Scan(&table); err != nil {
		t.Fatalf("inspect PostgreSQL state table: %v", err)
	}
	if table == nil {
		return nil, false
	}
	var data []byte
	if err := conn.QueryRow(context.Background(), "SELECT state::text FROM "+testStateTable+" WHERE id = true").Scan(&data); err != nil {
		if err == pgx.ErrNoRows {
			return nil, false
		}
		t.Fatalf("read PostgreSQL state: %v", err)
	}
	return data, true
}

func postgresTestStateTableExists(t *testing.T) bool {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	var table *string
	if err := conn.QueryRow(context.Background(), "SELECT to_regclass($1)", testStateTable).Scan(&table); err != nil {
		t.Fatalf("inspect PostgreSQL state table: %v", err)
	}
	return table != nil
}

func setPostgresTestStateJSON(t *testing.T, data []byte) {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	_, err := conn.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS public.marketwatch_state (
			id boolean PRIMARY KEY DEFAULT true CHECK (id),
			state jsonb NOT NULL
		)`)
	if err != nil {
		t.Fatalf("create test state table: %v", err)
	}
	_, err = conn.Exec(context.Background(), "INSERT INTO "+testStateTable+" (id, state) VALUES (true, $1::jsonb) ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state", string(data))
	if err != nil {
		t.Fatalf("seed PostgreSQL state: %v", err)
	}
}

func postgresTestState(t *testing.T) State {
	t.Helper()
	data, ok := postgresTestStateJSON(t)
	if !ok {
		t.Fatal("PostgreSQL state row is missing")
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode PostgreSQL state: %v", err)
	}
	return state
}

func postgresTestJSONEqual(t *testing.T, left, right []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(left, &a); err != nil {
		t.Fatalf("decode first JSONB snapshot: %v", err)
	}
	if err := json.Unmarshal(right, &b); err != nil {
		t.Fatalf("decode second JSONB snapshot: %v", err)
	}
	return reflect.DeepEqual(a, b)
}

func acquireMarketwatchAdvisoryLock(t *testing.T) *pgx.Conn {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_lock(hashtextextended(current_database() || ':marketwatch', 0))"); err != nil {
		t.Fatalf("acquire test advisory lock: %v", err)
	}
	return conn
}

func terminateMarketwatchBackends(t *testing.T) {
	t.Helper()
	conn := connectPostgresTest(t, "marketwatch_test")
	rows, err := conn.Query(context.Background(), `
		SELECT pid FROM pg_stat_activity
		WHERE datname = current_database()
		  AND application_name = 'marketwatch'
		  AND pid <> pg_backend_pid()`)
	if err != nil {
		t.Errorf("find Marketwatch database connection: %v", err)
		return
	}
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			t.Errorf("read Marketwatch database connection: %v", err)
			return
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Errorf("list Marketwatch database connections: %v", err)
		return
	}
	rows.Close()
	if len(pids) == 0 {
		t.Errorf("no active Marketwatch database connection to terminate")
		return
	}
	for _, pid := range pids {
		var terminated bool
		if err := conn.QueryRow(context.Background(), "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
			t.Errorf("terminate Marketwatch database connection: %v", err)
		}
	}
}

func envWithoutDatabaseSettings(t *testing.T) {
	t.Helper()
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD", "DB_SSLMODE"} {
		t.Setenv(key, "")
	}
}

func replacePostgresTestEnv(t *testing.T, base []string, overrides map[string]string) []string {
	t.Helper()
	keys := map[string]bool{}
	for key := range overrides {
		keys[key] = true
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[key] {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, fmt.Sprintf("%s=%s", key, value))
	}
	return result
}

type postgresTestProxy struct {
	listener   net.Listener
	target     string
	mu         sync.Mutex
	blocked    bool
	clients    map[net.Conn]struct{}
	wg         sync.WaitGroup
	loseCommit atomic.Bool
}

func newPostgresTestProxy(t *testing.T) *postgresTestProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backendPort := os.Getenv("DB_PORT")
	if backendPort == "" {
		backendPort = "5432"
	}
	p := &postgresTestProxy{
		listener: listener,
		target:   net.JoinHostPort(os.Getenv("DB_HOST"), backendPort),
		clients:  make(map[net.Conn]struct{}),
	}
	p.wg.Go(func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.blocked {
				p.mu.Unlock()
				_ = client.Close()
				continue
			}
			p.clients[client] = struct{}{}
			p.wg.Go(func() {
				defer client.Close()
				defer func() {
					p.mu.Lock()
					delete(p.clients, client)
					p.mu.Unlock()
				}()
				upstream, err := net.DialTimeout("tcp", p.target, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(upstream, client)
					_ = upstream.Close()
					close(done)
				}()
				p.copyResponses(client, upstream)
				_ = client.Close()
				<-done
			})
			p.mu.Unlock()
		}
	})
	t.Cleanup(func() {
		_ = listener.Close()
		p.block(true)
		p.wg.Wait()
	})
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", port)
	return p
}

// Drop a server-confirmed COMMIT before the client receives CommandComplete.
// PostgreSQL backend frames have a one-byte type, a length including its four
// bytes, and a payload; CommandComplete carries the zero-terminated command tag.
func (p *postgresTestProxy) copyResponses(client, upstream net.Conn) {
	for {
		var header [5]byte
		if _, err := io.ReadFull(upstream, header[:]); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(header[1:])
		if size < 4 || size > 16<<20 {
			return
		}
		payload := make([]byte, int(size)-4)
		if _, err := io.ReadFull(upstream, payload); err != nil {
			return
		}
		if header[0] == 'C' && string(payload) == "COMMIT\x00" && p.loseCommit.CompareAndSwap(true, false) {
			return
		}
		if _, err := client.Write(header[:]); err != nil {
			return
		}
		if _, err := client.Write(payload); err != nil {
			return
		}
	}
}

func (p *postgresTestProxy) block(blocked bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = blocked
	if blocked {
		for client := range p.clients {
			_ = client.Close()
		}
	}
}

func TestE2ECommitResponseLostAfterAcceptedSend(t *testing.T) {
	h := newHarness(t, fourRules)
	h.prices("100", "5", "0", "50", "0", "-10")
	proxy := newPostgresTestProxy(t)
	h.beforePush = func() {
		if len(h.messages) == 1 {
			proxy.loseCommit.Store(true)
		}
	}
	err := h.run(true)
	h.sent(1)
	if !strings.Contains(err.Error(), "commit state save") || proxy.loseCommit.Load() {
		t.Fatalf("expected a server-confirmed COMMIT with a lost response; err=%v still_armed=%v", err, proxy.loseCommit.Load())
	}
	state := h.state()
	firstKey := ruleKey("90", "above", 100, "")
	if state.Rules[firstKey].Pending != nil {
		t.Fatal("committed accepted notification remained pending")
	}
	for _, key := range []string{
		ruleKey("80", "below", 50, ""),
		ruleKey("90", "absolute-change", 5, "24h"),
		ruleKey("80", "absolute-change", 10, "7d"),
	} {
		if state.Rules[key].Pending == nil {
			t.Fatalf("later notification %s was not retained after lost COMMIT response", key)
		}
	}

	h.beforePush = nil
	h.run(false)
	h.sent(4) // The acknowledged first notification was committed and is not duplicated.
	for key, rule := range h.state().Rules {
		if rule.Pending != nil {
			t.Fatalf("retry left pending notification %s", key)
		}
	}
}
