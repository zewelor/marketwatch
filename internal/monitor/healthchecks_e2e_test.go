package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type healthchecksEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *healthchecksEvents) record(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *healthchecksEvents) check(h *harness, want []string) {
	h.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	data, err := json.MarshalIndent(map[string]any{"events": e.events, "logs": h.logs.String()}, "", "  ")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "healthchecks.json"), data, 0600); err != nil {
		h.t.Fatal(err)
	}
	if !reflect.DeepEqual(e.events, want) {
		h.t.Fatalf("events=%v want=%v", e.events, want)
	}
	if strings.Contains(h.logs.String(), "fixture-check") || strings.Contains(h.logs.String(), "fixture-response") {
		h.t.Fatal("Healthchecks URL or response leaked in logs")
	}
}

func TestE2EHealthchecksLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "market_failure", "partial_failure", "push_failure", "config_failure", "database_failure", "disabled", "dry_run", "dry_run_failure"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t, oneRule)
			events := &healthchecksEvents{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.RawQuery != "source=fixture" {
					t.Error("unexpected ping method or query")
				}
				events.record(req.URL.Path)
				fmt.Fprint(w, "OK")
			}))
			defer server.Close()
			h.options.HealthchecksPingURL = server.URL + "/fixture-check/?source=fixture"
			h.beforeMarket = func() { events.record("market") }
			h.beforePush = func() {
				if h.state().Rules[ruleKey("90", "absolute-change", 5, "24h")].Pending == nil {
					t.Error("Pushover send preceded pending commit")
				}
				events.record("push")
			}
			want := []string{"/fixture-check/start", "market", "push", "/fixture-check"}
			wantError := false
			switch scenario {
			case "market_failure":
				h.marketStatus = 503
				wantError = true
				want = []string{"/fixture-check/start", "market", "/fixture-check/fail"}
			case "partial_failure":
				h.config("rules:\n - {coin: btc, threshold: 5, window: 24h}\n - {coin: eth, above: 40}\n")
				h.prices("100", "invalid", "0", "50", "0", "0")
				h.beforePush = func() { events.record("push") }
				wantError = true
				want[3] = "/fixture-check/fail"
			case "push_failure":
				h.pushStatus = 503
				wantError = true
				want[3] = "/fixture-check/fail"
			case "config_failure", "database_failure":
				if scenario == "config_failure" {
					h.config("rules: []")
				} else {
					t.Setenv("DB_PORT", "0")
				}
				wantError = true
				want = []string{"/fixture-check/start", "/fixture-check/fail"}
			case "disabled":
				h.options.HealthchecksPingURL = ""
				want = []string{"market", "push"}
			case "dry_run", "dry_run_failure":
				h.options.DryRun = true
				want = []string{"market"}
				if scenario == "dry_run_failure" {
					h.marketStatus = 503
					wantError = true
				}
			}
			// The harness reads state for its artifacts with the test DB settings.
			if scenario == "database_failure" {
				err := h.runner.Run(context.Background(), h.options)
				if err == nil {
					t.Fatal("invalid database port succeeded")
				}
			} else {
				h.run(wantError)
			}
			events.check(h, want)
			if h.options.DryRun && postgresTestStateTableExists(t) {
				t.Fatal("dry-run created state")
			}
		})
	}
}

func TestE2EHealthchecksOutage(t *testing.T) {
	for _, scenario := range []string{"http_failure", "redirect", "timeout", "connection_failure", "invalid_url"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t, oneRule)
			events := &healthchecksEvents{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				events.record(req.URL.Path)
				switch scenario {
				case "timeout":
					<-req.Context().Done()
					return
				case "redirect":
					http.Redirect(w, req, "/unexpected-redirect", http.StatusFound)
				default:
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, "fixture-response")
				}
			}))
			defer server.Close()
			h.options.HealthchecksPingURL = server.URL + "/fixture-check"
			if scenario == "connection_failure" {
				server.Close()
			}
			if scenario == "invalid_url" {
				h.options.HealthchecksPingURL = "://fixture-check"
			}
			start := time.Now()
			h.run(false)
			if elapsed := time.Since(start); elapsed > 7*time.Second {
				t.Fatalf("Healthchecks outage delayed check for %v", elapsed)
			}
			h.sent(1)
			if h.gets != 1 || h.state().Rules[ruleKey("90", "absolute-change", 5, "24h")].Pending != nil {
				t.Fatal("Healthchecks failure blocked market check or delivery commit")
			}
			if strings.Count(h.logs.String(), `"level":"WARN"`) != 2 {
				t.Fatalf("expected two ping warnings: %s", h.logs.String())
			}
			var want []string
			if scenario != "connection_failure" && scenario != "invalid_url" {
				want = []string{"/fixture-check/start", "/fixture-check"}
			}
			events.check(h, want)
		})
	}
}

func TestE2EHealthchecksCancelledCheck(t *testing.T) {
	h := newHarness(t, oneRule)
	events := &healthchecksEvents{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		events.record(req.URL.Path)
		fmt.Fprint(w, "OK")
	}))
	defer server.Close()
	h.options.HealthchecksPingURL = server.URL + "/fixture-check"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.beforeMarket = cancel
	if err := h.runner.Run(ctx, h.options); err == nil {
		t.Fatal("cancelled market check succeeded")
	}
	events.check(h, []string{"/fixture-check/start", "/fixture-check/fail"})
}
