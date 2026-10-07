package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This harness exercises the production runner, real HTTP and on-disk state.
// Only clock and HTTP destinations differ from CLI. Fixtures contain no secrets.
type harness struct {
	t                                    *testing.T
	runner                               *Runner
	options                              Options
	now                                  time.Time
	market                               string
	marketStatus, pushStatus, pushResult int
	gets                                 int
	ids                                  string
	messages                             []url.Values
	logs                                 bytes.Buffer
	dir                                  string
	step                                 int
	beforePush                           func()
	beforeMarket                         func()
}

func newHarness(t *testing.T, config string) *harness {
	t.Helper()
	h := &harness{t: t, now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), marketStatus: 200, pushStatus: 200, pushResult: 1}
	h.dir = t.TempDir()
	if root := os.Getenv("MARKETWATCH_ARTIFACT_DIR"); root != "" {
		absolute, err := filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(absolute, 0700); err != nil {
			t.Fatal(err)
		}
		h.dir, err = os.MkdirTemp(absolute, strings.ReplaceAll(t.Name(), "/", "_")+"-")
		if err != nil {
			t.Fatal(err)
		}
	}
	h.options = Options{ConfigPath: filepath.Join(h.dir, "config.yaml"), StatePath: filepath.Join(h.dir, "data", "state.json"), Token: "fixture-token", User: "fixture-user"}
	h.config(config)
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.gets++
		h.ids = r.URL.Query().Get("id")
		if h.beforeMarket != nil {
			h.beforeMarket()
		}
		w.WriteHeader(h.marketStatus)
		fmt.Fprint(w, h.market)
	}))
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form := r.PostForm
		if form.Get("token") != "fixture-token" || form.Get("user") != "fixture-user" || form.Get("priority") != "0" {
			t.Error("invalid Pushover request")
		}
		form.Del("token")
		form.Del("user")
		h.messages = append(h.messages, form)
		if h.beforePush != nil {
			h.beforePush()
		}
		w.WriteHeader(h.pushStatus)
		fmt.Fprintf(w, `{"status":%d}`, h.pushResult)
	}))
	t.Cleanup(market.Close)
	t.Cleanup(push.Close)
	h.runner = New(slog.New(slog.NewJSONHandler(&h.logs, nil)))
	h.runner.coinURL = market.URL
	h.runner.pushURL = push.URL
	h.runner.Now = func() time.Time { return h.now }
	h.prices("100", "5", "-10", "50", "-5", "10")
	return h
}

func (h *harness) config(config string) {
	h.t.Helper()
	if err := os.WriteFile(h.options.ConfigPath, []byte(config), 0600); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) prices(bp, bd, bw, ep, ed, ew string) {
	h.market = fmt.Sprintf("[{\"id\":\"80\",\"price_usd\":%q,\"percent_change_24h\":%q,\"percent_change_7d\":%q},{\"id\":\"90\",\"price_usd\":%q,\"percent_change_24h\":%q,\"percent_change_7d\":%q}]", ep, ed, ew, bp, bd, bw)
}
func (h *harness) run(wantError bool) error {
	h.t.Helper()
	h.step++
	h.logs.Reset()
	err := h.runner.Run(context.Background(), h.options)
	if (err != nil) != wantError {
		h.t.Fatalf("step %d: error=%v, wantError=%v; logs=%s", h.step, err, wantError, h.logs.String())
	}
	artifact := map[string]any{"step": h.step, "time": h.now, "error": fmt.Sprint(err), "expected_error": wantError, "logs": json.RawMessage("[]"), "market": h.market, "requests": h.messages}
	lines := strings.Split(strings.TrimSpace(h.logs.String()), "\n")
	var logs []json.RawMessage
	for _, line := range lines {
		if line != "" {
			if !json.Valid([]byte(line)) {
				h.t.Fatalf("non-JSON log: %s", line)
			}
			logs = append(logs, json.RawMessage(line))
		}
	}
	artifact["logs"] = logs
	if state, readErr := os.ReadFile(h.options.StatePath); readErr == nil {
		artifact["state"] = string(state)
	}
	b, marshalErr := json.MarshalIndent(artifact, "", "  ")
	if marshalErr != nil {
		h.t.Fatal(marshalErr)
	}
	if err := os.WriteFile(filepath.Join(h.dir, fmt.Sprintf("step-%02d.json", h.step)), b, 0600); err != nil {
		h.t.Fatal(err)
	}
	if strings.Contains(h.logs.String(), "fixture-token") || strings.Contains(h.logs.String(), "fixture-user") {
		h.t.Fatal("secret in log")
	}
	return err
}
func (h *harness) state() State {
	h.t.Helper()
	data, err := os.ReadFile(h.options.StatePath)
	if err != nil {
		h.t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		h.t.Fatal(err)
	}
	return state
}
func (h *harness) sent(n int) {
	h.t.Helper()
	if len(h.messages) != n {
		h.t.Fatalf("messages=%d want=%d", len(h.messages), n)
	}
}

const fourRules = `rules:
  - {id: high, coin: btc, above: 100}
  - {id: low, coin: "80", below: 50}
  - {id: day, coin: "90", threshold: 5, window: 24h}
  - {id: week, coin: "80", threshold: 10, window: 7d}
`
const oneRule = `rules:
  - {id: high, coin: "90", threshold: 5, window: 24h}
`

func TestE2EEpisodes(t *testing.T) {
	h := newHarness(t, fourRules)
	h.prices("100", "5", "0", "50", "0", "-10")
	h.beforePush = func() {
		s := h.state()
		if s.Version != 2 || !s.Rules["high"].Active || !s.Rules["week"].Active {
			t.Error("all evaluations must persist before first send")
		}
	}
	h.run(false)
	h.sent(4)
	if h.ids != "90,80" {
		t.Fatalf("dedup/canonical IDs: %q", h.ids)
	}
	for _, m := range h.messages {
		if m.Get("url") != "" || m.Get("url_title") != "" {
			t.Error("unexpected attribution")
		}
		if m.Get("title") != "Marketwatch" || !strings.Contains(m.Get("message"), "CoinLore") || !strings.Contains(m.Get("message"), "USD") || !strings.Contains(m.Get("message"), "2026-10-07T12:00:00Z") || !strings.Contains(m.Get("message"), "condition observed") || !strings.Contains(m.Get("message"), "Data fetched:") || !strings.Contains(m.Get("message"), "Fetch time does not confirm price freshness.") {
			t.Fatalf("message: %v", m)
		}
	}
	h.beforePush = nil
	h.now = h.now.Add(time.Hour)
	h.run(false)
	h.sent(4)
	// Exactly at rearm margins remains active.
	h.prices("99.5", "4.5", "0", "50.25", "0", "-9.5")
	h.run(false)
	h.sent(4)
	for _, r := range h.state().Rules {
		if !r.Active {
			t.Error("equality rearmed")
		}
	}
	h.prices("99.49", "4.49", "0", "50.26", "0", "-9.49")
	h.run(false)
	h.sent(4)
	for _, r := range h.state().Rules {
		if r.Active {
			t.Error("strict margin failed to rearm")
		}
	}
	h.prices("100", "5", "0", "50", "0", "-10")
	h.run(false)
	h.sent(8)
	if !strings.Contains(h.messages[4].Get("message"), "condition observed") {
		t.Error("later episode must describe an observation")
	}
	// YAML order and canonical spelling do not reset.
	h.config("rules:\n - {id: week, coin: \"80\", threshold: 10, window: 7d}\n - {id: day, coin: \"90\", threshold: 5, window: 24h}\n - {id: low, coin: \"80\", below: 50}\n - {id: high, coin: \"90\", above: 100}\n")
	h.run(false)
	h.sent(8)
}

func TestE2EPendingSequences(t *testing.T) {
	h := newHarness(t, oneRule)
	h.pushStatus = 503
	h.run(true)
	h.sent(1)
	original := h.state().Rules["high"].Pending.Message
	h.now = h.now.Add(time.Hour)
	h.prices("99", "0", "0", "50", "0", "0")
	h.run(true)
	h.sent(2)
	if h.state().Rules["high"].Active || h.state().Rules["high"].Pending == nil {
		t.Fatal("rearm removed pending")
	}
	h.now = h.now.Add(time.Hour)
	h.prices("101", "-5", "0", "50", "0", "0")
	h.run(true)
	h.sent(3)
	p := h.state().Rules["high"].Pending
	if p.Message == original || !p.ObservedAt.Equal(h.now) || !strings.Contains(h.logs.String(), "pending_replaced") {
		t.Fatal("new episode did not replace pending")
	}
	h.pushStatus = 200
	h.marketStatus = 503
	h.now = h.now.Add(time.Hour)
	h.run(true)
	h.sent(4)
	if h.messages[3].Get("message") != p.Message || h.messages[3].Get("title") != "Marketwatch" || h.state().Rules["high"].Pending != nil {
		t.Fatal("retry must use persisted message and save success despite market failure")
	}
	h.marketStatus = 200
	h.run(false)
	h.sent(4)
}

func TestE2EExpiryAndConfigChange(t *testing.T) {
	h := newHarness(t, fourRules)
	h.prices("100", "5", "0", "50", "0", "-10")
	h.pushStatus = 400
	h.run(true)
	h.sent(1)
	for _, r := range h.state().Rules {
		if r.Pending == nil {
			t.Fatal("4xx prevented evaluation")
		}
	}
	h.now = h.now.Add(3 * time.Hour)
	h.run(true)
	h.sent(1)
	for _, r := range h.state().Rules {
		if r.Pending != nil || !r.Active {
			t.Fatal("expiry reset episode")
		}
	}
	h.pushStatus = 200
	h.config("rules:\n - {id: high, coin: \"90\", threshold: 4, window: 24h}\n - {id: day, coin: \"90\", threshold: 5, window: 24h}\n")
	h.run(false)
	h.sent(2)
	if len(h.state().Rules) != 2 || !strings.Contains(h.messages[1].Get("message"), "condition observed") {
		t.Fatal("config reconciliation")
	}
}

func TestE2EPartialData(t *testing.T) {
	cases := []struct {
		name, market string
		sent         int
	}{
		{"missing", "[{\"id\":\"90\",\"price_usd\":\"100\",\"percent_change_24h\":\"5\"}]", 2},
		{"duplicate", "[{\"id\":\"90\",\"price_usd\":\"100\"},{\"id\":\"90\",\"price_usd\":\"100\"},{\"id\":\"80\",\"price_usd\":\"50\",\"percent_change_7d\":\"-10\"}]", 2},
		{"bad_price", "[{\"id\":\"90\",\"price_usd\":\"NaN\",\"percent_change_24h\":\"5\"},{\"id\":\"80\",\"price_usd\":\"50\",\"percent_change_7d\":\"-10\"}]", 2},
		{"window_null", "[{\"id\":\"90\",\"price_usd\":\"100\",\"percent_change_24h\":null},{\"id\":\"80\",\"price_usd\":\"50\",\"percent_change_7d\":\"-10\"}]", 3},
		{"window_number", "[{\"id\":\"90\",\"price_usd\":\"100\",\"percent_change_24h\":5},{\"id\":\"80\",\"price_usd\":\"50\",\"percent_change_7d\":\"-10\"}]", 3},
		{"bad_json", "[", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { h := newHarness(t, fourRules); h.market = c.market; h.run(true); h.sent(c.sent) })
	}
	// 0% is a valid change, not missing; both directions on both windows.
	t.Run("zero_and_other_windows", func(t *testing.T) {
		h := newHarness(t, "rules:\n - {id: down-day, coin: \"90\", threshold: 5, window: 24h}\n - {id: up-week, coin: \"80\", threshold: 10, window: 7d}\n")
		h.prices("100", "0", "0", "50", "0", "0")
		h.run(false)
		h.sent(0)
		h.prices("100", "-5", "0", "50", "0", "10")
		h.run(false)
		h.sent(2)
	})
}

func TestE2EInvalidConfigBeforeIO(t *testing.T) {
	cases := []string{
		"rules: []",
		strings.Replace(oneRule, "threshold: 5", "threshold: .inf", 1),
		strings.Replace(oneRule, "threshold: 5", "threshold: 0", 1),
		strings.Replace(oneRule, "coin: \"90\"", "coin: 90", 1),
		strings.Replace(oneRule, "coin: \"90\"", "coin: \"0\"", 1),
		strings.Replace(oneRule, "window: 24h", "window: 24h, kind: up", 1),
		strings.Replace(oneRule, "window: 24h", "window: 1h", 1),
		strings.Replace(oneRule, ", window: 24h", "", 1),
		strings.Replace(oneRule, "threshold: 5", "threshold: 5, currency: USD", 1),
		strings.Replace(oneRule, "threshold: 5", "threshold: 5, threshold: 6", 1),
		oneRule + "---\n" + oneRule,
		oneRule + "  - {id: high, coin: \"80\", threshold: 1, window: 7d}\n",
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newHarness(t, c)
			h.run(true)
			if h.gets != 0 {
				t.Fatal("network before validation")
			}
			if _, err := os.Stat(filepath.Dir(h.options.StatePath)); !os.IsNotExist(err) {
				t.Fatal("created state dir before validation")
			}
		})
	}
}

func TestE2EStateAndDryRun(t *testing.T) {
	t.Run("dry_new", func(t *testing.T) {
		h := newHarness(t, oneRule)
		h.options.DryRun = true
		h.options.Token = ""
		h.options.User = ""
		h.run(false)
		h.sent(0)
		if _, err := os.Stat(filepath.Dir(h.options.StatePath)); !os.IsNotExist(err) {
			t.Fatal("dry-run mkdir")
		}
		if !strings.Contains(h.logs.String(), "CoinLore") || !strings.Contains(h.logs.String(), "fetched_at") {
			t.Fatal("dry-run missing source/time")
		}
	})
	t.Run("dry_existing", func(t *testing.T) {
		h := newHarness(t, oneRule)
		h.pushStatus = 503
		h.run(true)
		before, _ := os.ReadFile(h.options.StatePath)
		h.options.DryRun = true
		h.options.Token = ""
		h.options.User = ""
		h.run(false)
		after, _ := os.ReadFile(h.options.StatePath)
		if !bytes.Equal(before, after) {
			t.Fatal("dry-run changed state")
		}
		h.sent(1)
	})
	for i, data := range []string{"", "{}", "{", "{\"version\":3,\"rules\":{}}", "{\"version\":2,\"rules\":null}", "{\"version\":2,\"rules\":{\"high\":{}}}", "{\"version\":2,\"version\":2,\"rules\":{}}"} {
		t.Run(fmt.Sprintf("corrupt_%d", i), func(t *testing.T) {
			h := newHarness(t, oneRule)
			if err := os.MkdirAll(filepath.Dir(h.options.StatePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.options.StatePath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			h.run(true)
			if h.gets != 0 {
				t.Fatal("corrupt state used network")
			}
			after, _ := os.ReadFile(h.options.StatePath)
			if string(after) != data {
				t.Fatal("corrupt state overwritten")
			}
		})
	}
	t.Run("lock", func(t *testing.T) {
		h := newHarness(t, oneRule)
		if err := os.MkdirAll(filepath.Dir(h.options.StatePath), 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(h.options.StatePath+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		h.run(true)
		if h.gets != 0 {
			t.Fatal("conflicting check used network")
		}
		h.options.DryRun = true
		h.run(false)
	})
	t.Run("write_before_send", func(t *testing.T) {
		h := newHarness(t, oneRule)
		h.beforeMarket = func() {
			if err := os.Mkdir(h.options.StatePath, 0700); err != nil {
				t.Error(err)
			}
		}
		h.run(true)
		h.sent(0)
		if h.gets != 1 || !strings.Contains(h.logs.String(), "rule_evaluated") {
			t.Fatal("must fail after network/evaluation, before send")
		}
	})
	t.Run("write_after_send", func(t *testing.T) {
		h := newHarness(t, fourRules)
		h.prices("100", "5", "0", "50", "0", "-10")
		dataDir := filepath.Dir(h.options.StatePath)
		backup := dataDir + "-backup"
		h.beforePush = func() {
			if len(h.messages) == 1 {
				if err := os.Rename(dataDir, backup); err != nil {
					t.Error(err)
					return
				}
				if err := os.WriteFile(dataDir, []byte("obstruction"), 0600); err != nil {
					t.Error(err)
				}
			}
		}
		h.run(true)
		h.sent(1)
		h.beforePush = nil
		if err := os.Remove(dataDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, dataDir); err != nil {
			t.Fatal(err)
		}
		for _, r := range h.state().Rules {
			if r.Pending == nil {
				t.Fatal("pre-send state did not retain pending")
			}
		}
		h.run(false)
		h.sent(5) // The accepted-but-unpersisted first alert can duplicate.
	})
	t.Run("cancel", func(t *testing.T) {
		h := newHarness(t, oneRule)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := h.runner.Run(ctx, h.options); err == nil {
			t.Fatal("cancel ignored")
		}
		h.sent(0)
	})
	t.Run("status_zero", func(t *testing.T) {
		h := newHarness(t, oneRule)
		h.pushResult = 0
		h.run(true)
		if h.state().Rules["high"].Pending == nil {
			t.Fatal("status zero treated as accepted")
		}
	})
}

func TestCLISmoke(t *testing.T) {
	h := newHarness(t, "rules: []")
	binary := filepath.Join(t.TempDir(), "marketwatch")
	cmd := exec.Command("go", "build", "-o", binary, "../../cmd/marketwatch")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, args := range [][]string{{}, {"check", "--unknown"}, {"check", "--config", h.options.ConfigPath, "--state", h.options.StatePath, "--dry-run"}} {
		cmd = exec.Command(binary, args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("invalid CLI succeeded: %v", args)
		}
		if len(out) == 0 {
			t.Fatal("CLI failure without explanation")
		}
		if err := os.WriteFile(filepath.Join(h.dir, fmt.Sprintf("cli-%d.json", len(args))), out, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCLIEnvironment(t *testing.T) {
	h := newHarness(t, oneRule)
	binary := filepath.Join(t.TempDir(), "marketwatch")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/marketwatch")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	state := filepath.Join(h.dir, "corrupt-state.json")
	alternate := filepath.Join(h.dir, "unsupported-state.json")
	invalidConfig := filepath.Join(h.dir, "invalid-config.yaml")
	for path, contents := range map[string]string{state: "{", alternate: `{"version":3,"rules":{}}`, invalidConfig: "rules: []"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "MARKETWATCH_CONFIG", "MARKETWATCH_STATE", "MARKETWATCH_DRY_RUN", "PUSHOVER_APP_TOKEN", "PUSHOVER_USER_KEY":
			continue
		}
		base = append(base, entry)
	}
	cases := []struct {
		name                       string
		args                       []string
		config, state, dry, reason string
	}{
		{"env_only", nil, h.options.ConfigPath, state, "true", "invalid state"},
		{"config_flag", []string{"--config", invalidConfig}, h.options.ConfigPath, state, "true", "nonempty rules list"},
		{"state_flag", []string{"--state", alternate}, h.options.ConfigPath, state, "true", "unsupported state version"},
		{"dry_false_flag", []string{"--dry-run=false"}, h.options.ConfigPath, state, "true", "PUSHOVER_APP_TOKEN and PUSHOVER_USER_KEY are required"},
		{"dry_true_flag", []string{"--dry-run"}, h.options.ConfigPath, state, "false", "invalid state"},
		{"bad_dry", nil, h.options.ConfigPath, state, "yes", "MARKETWATCH_DRY_RUN"},
		{"empty_dry_is_false", nil, h.options.ConfigPath, state, "", "PUSHOVER_APP_TOKEN and PUSHOVER_USER_KEY are required"},
		{"numeric_true", nil, h.options.ConfigPath, state, "1", "invalid state"},
		{"empty_config", nil, "", state, "true", "config and state paths must be nonempty"},
		{"empty_state", nil, h.options.ConfigPath, "", "true", "config and state paths must be nonempty"},
		{"flag_overrides_bad_env", []string{"--config", h.options.ConfigPath, "--state", state, "--dry-run"}, "", "", "bad", "invalid state"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"check"}, c.args...)
			cmd := exec.Command(binary, args...)
			cmd.Env = append(append([]string{}, base...), "MARKETWATCH_CONFIG="+c.config, "MARKETWATCH_STATE="+c.state, "MARKETWATCH_DRY_RUN="+c.dry)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), c.reason) {
				t.Fatalf("error=%v output=%s want=%s", err, out, c.reason)
			}
			if err := os.WriteFile(filepath.Join(h.dir, "env-"+c.name+".jsonl"), out, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	for path, expected := range map[string]string{state: "{", alternate: `{"version":3,"rules":{}}`} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != expected {
			t.Fatal("CLI changed corrupt fixture state")
		}
		if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
			t.Fatal("CLI created a lock in dry-run or before rejecting secrets/ENV")
		}
	}
}

func TestE2ECaseVariantState(t *testing.T) {
	for _, variant := range []string{"version", "active", "pending"} {
		t.Run(variant, func(t *testing.T) {
			h := newHarness(t, oneRule)
			h.pushStatus = 503
			h.run(true)
			data, err := os.ReadFile(h.options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "version":
				data = bytes.Replace(data, []byte(`"version": 2`), []byte(`"version": 3, "Version": 2`), 1)
			case "active":
				data = bytes.Replace(data, []byte(`"active": true`), []byte(`"active": true, "Active": false`), 1)
			case "pending":
				data = bytes.Replace(data, []byte(`"message":`), []byte(`"Message": "ambiguous", "message":`), 1)
			}
			if err := os.WriteFile(h.options.StatePath, data, 0600); err != nil {
				t.Fatal(err)
			}
			gets := h.gets
			h.run(true)
			h.sent(1)
			after, err := os.ReadFile(h.options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			if h.gets != gets || !bytes.Equal(data, after) {
				t.Fatal("ambiguous state used network or was overwritten")
			}
		})
	}
}

func TestE2EInvalidMinimalState(t *testing.T) {
	for _, variant := range []string{"legacy", "missing_active", "null_active", "unknown_rule", "unknown_pending", "missing_time", "empty_message"} {
		t.Run(variant, func(t *testing.T) {
			h := newHarness(t, oneRule)
			h.pushStatus = 503
			h.run(true)
			original, err := os.ReadFile(h.options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal(original, &state); err != nil {
				t.Fatal(err)
			}
			rule := state["rules"].(map[string]any)["high"].(map[string]any)
			pending := rule["pending"].(map[string]any)
			switch variant {
			case "legacy":
				state["version"] = 1
				rule["checked"] = true
				pending["title"] = "Marketwatch"
			case "missing_active":
				delete(rule, "active")
			case "null_active":
				rule["active"] = nil
			case "unknown_rule":
				rule["unexpected"] = true
			case "unknown_pending":
				pending["unexpected"] = "value"
			case "missing_time":
				delete(pending, "observed_at")
			case "empty_message":
				pending["message"] = ""
			}
			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.options.StatePath, data, 0600); err != nil {
				t.Fatal(err)
			}
			gets := h.gets
			runErr := h.run(true)
			h.sent(1)
			after, err := os.ReadFile(h.options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			if h.gets != gets || !bytes.Equal(data, after) {
				t.Fatal("invalid or legacy state used network or was overwritten")
			}
			if variant == "legacy" && !strings.Contains(runErr.Error(), "unsupported state version 1") {
				t.Fatal("legacy state rejected without a clear version error")
			}
		})
	}
}

func TestE2ECombinedFailureAndRestart(t *testing.T) {
	h := newHarness(t, fourRules)
	// ETH has a usable price but missing 7d; BTC has both price and 24h.
	h.market = `[{"id":"90","price_usd":"100","percent_change_24h":"5"},{"id":"80","price_usd":"50","percent_change_7d":null}]`
	h.pushStatus = 503
	h.run(true)
	h.sent(3)
	if r := h.state().Rules["week"]; r.Active || r.Pending != nil {
		t.Fatal("missing 7d changed the rule")
	}
	h.now = h.now.Add(time.Hour)
	h.prices("99", "4", "0", "51", "0", "-9")
	h.run(true)
	h.sent(6)
	h.now = h.now.Add(time.Hour)
	h.prices("101", "6", "0", "49", "0", "-10")
	h.run(true)
	h.sent(10)
	// Restart runner, retaining only the durable state.
	logger := h.runner.Logger
	coinURL, pushURL := h.runner.coinURL, h.runner.pushURL
	h.runner = New(logger)
	h.runner.Now = func() time.Time { return h.now }
	h.runner.coinURL, h.runner.pushURL = coinURL, pushURL
	h.pushStatus = 200
	h.now = h.now.Add(30 * time.Minute)
	h.run(false)
	h.sent(14)
	h.config("rules:\n - {id: high, coin: \"90\", above: 100}\n - {id: day, coin: \"90\", threshold: 7, window: 24h}\n")
	h.run(false)
	h.sent(14)
	if len(h.state().Rules) != 2 || h.state().Rules["day"].Active {
		t.Fatal("definition reconciliation after restart")
	}
}

func TestE2EHTTPTimeout(t *testing.T) {
	h := newHarness(t, oneRule)
	h.pushStatus = 503
	h.run(true)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	}))
	defer stalled.Close()
	h.runner.Client.Timeout = 30 * time.Millisecond
	h.runner.coinURL = stalled.URL
	h.pushStatus = 200
	h.run(true)
	h.sent(2)
	if h.state().Rules["high"].Pending != nil {
		t.Fatal("market timeout blocked retry")
	}
}

func TestE2EBidirectionalMovement(t *testing.T) {
	h := newHarness(t, "rules:\n - {id: move, coin: BTC, threshold: 3, window: 24h}\n")
	for i, step := range []struct {
		change string
		sent   int
		active bool
	}{
		{"2.99", 0, false}, {"3", 1, true}, {"-3", 1, true},
		{"-2.5", 1, true}, {"-2.49", 1, false}, {"-3", 2, true},
		{"2.5", 2, true}, {"2.49", 2, false}, {"3", 3, true},
	} {
		h.now = h.now.Add(time.Minute)
		h.prices("100", step.change, "0", "50", "0", "0")
		h.run(false)
		h.sent(step.sent)
		if h.state().Rules["move"].Active != step.active {
			t.Fatalf("step %d: wrong activity", i)
		}
	}
	if !strings.Contains(h.messages[1].Get("message"), "provider change: -3%") {
		t.Fatal("missing signed change")
	}
}

func TestE2ESmallMovementRearmsAtZero(t *testing.T) {
	h := newHarness(t, "rules:\n - {id: move, coin: eth, threshold: 0.3, window: 7d}\n")
	h.prices("100", "0", "0", "50", "0", "0.3")
	h.run(false)
	h.sent(1)
	h.prices("100", "0", "0", "50", "0", "0")
	h.run(false)
	if h.state().Rules["move"].Active {
		t.Fatal("small threshold did not rearm at zero")
	}
	h.prices("100", "0", "0", "50", "0", "-0.3")
	h.run(false)
	h.sent(2)
}

func TestE2EPriceAndPercentageEpisodes(t *testing.T) {
	h := newHarness(t, `rules:
 - {id: high, coin: btc, above: 86000}
 - {id: low, coin: btc, below: 80000}
 - {id: move, coin: btc, threshold: 3, window: 24h}
`)
	for i, step := range []struct {
		price, change   string
		sent            int
		high, low, move bool
	}{
		{"86000", "3", 2, true, false, true},
		{"90000", "-3", 2, true, false, true},
		{"85570", "2.5", 2, true, false, true},
		{"85569", "2.49", 2, false, false, false},
		{"80000", "-3", 4, false, true, true},
		{"80400", "0", 4, false, true, false},
		{"80401", "0", 4, false, false, false},
		{"86000", "3", 6, true, false, true},
	} {
		h.now = h.now.Add(time.Minute)
		h.prices(step.price, step.change, "0", "50", "0", "0")
		h.run(false)
		h.sent(step.sent)
		state := h.state()
		if state.Rules["high"].Active != step.high || state.Rules["low"].Active != step.low || state.Rules["move"].Active != step.move {
			t.Fatalf("step %d: wrong activity", i)
		}
	}
	if !strings.Contains(h.messages[0].Get("message"), "price >= 86000 USD") || !strings.Contains(h.messages[2].Get("message"), "price <= 80000 USD") {
		t.Fatal("missing price condition")
	}
	// Changing condition preserves unchanged percentage activity and resets high only.
	before := h.state().Rules["move"].Fingerprint
	h.config(`rules:
 - {id: high, coin: btc, threshold: 4, window: 24h}
 - {id: low, coin: btc, below: 80000}
 - {id: move, coin: btc, threshold: 3, window: 24h}
`)
	h.run(false)
	h.sent(6)
	state := h.state()
	if state.Rules["high"].Active || !state.Rules["move"].Active || state.Rules["move"].Fingerprint != before || !strings.Contains(h.logs.String(), "rule_reset") {
		t.Fatal("condition reconciliation")
	}
}

func TestE2EInvalidPriceConditionsBeforeIO(t *testing.T) {
	for i, condition := range []string{
		"above: 0", "below: -1", "above: .inf", "below: .nan", "above: null", "above: nope",
		"above: 86000, below: 80000", "above: 86000, threshold: 3, window: 24h",
		"above: 86000, window: 24h", "below: 80000, window: null", "",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newHarness(t, "rules:\n - {id: price, coin: btc, "+condition+"}\n")
			h.run(true)
			if h.gets != 0 {
				t.Fatal("invalid condition used network")
			}
			if _, err := os.Stat(filepath.Dir(h.options.StatePath)); !os.IsNotExist(err) {
				t.Fatal("invalid condition wrote state")
			}
		})
	}
}
