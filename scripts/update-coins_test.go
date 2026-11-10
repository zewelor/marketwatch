package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marketwatch/internal/catalog"
)

func TestCatalogRefreshSequence(t *testing.T) {
	response := `{"data":[{"id":"1","symbol":"X"},{"id":"2","symbol":"Y"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header")
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	old := []byte(`[{"id":"1","symbol":"LTC"},{"id":"2","symbol":"DOGE"}]`)
	path := newCatalogFile(t, old)
	err := refresh(context.Background(), newClient(), server.URL, path)
	if err == nil || !strings.Contains(err.Error(), `1: "LTC" -> "X"`) || !strings.Contains(err.Error(), `2: "DOGE" -> "Y"`) {
		t.Fatalf("expected both symbol changes, got %v", err)
	}
	var blocked *symbolChangesError
	if !errors.As(err, &blocked) {
		t.Fatalf("symbol changes must have a distinct result, got %v", err)
	}
	assertFile(t, path, old)
	t.Logf("changed symbols rejected: %v", err)

	// Removing an old ID and adding an ID is permitted; incoming IDs are canonicalized and sorted.
	response = `{"data":[{"id":"003","symbol":"NEW"},{"id":"01","symbol":"LTC"}]}`
	if err := refresh(context.Background(), newClient(), server.URL, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0] != (catalog.Entry{ID: "1", Symbol: "LTC"}) || entries[1] != (catalog.Entry{ID: "3", Symbol: "NEW"}) {
		t.Fatalf("unexpected updated catalog: %s", data)
	}
	t.Logf("updated snapshot: %s", data)

	// A broken response must not destroy the successfully updated snapshot.
	response = `{"data":[{"id":"1","symbol":"LTC"},{"id":"01","symbol":"LTC"}]}`
	if err := refresh(context.Background(), newClient(), server.URL, path); err == nil || !strings.Contains(err.Error(), "duplicate ID 1") {
		t.Fatalf("expected duplicate ID error, got %v", err)
	}
	assertFile(t, path, data)
}

func TestRefreshUnchangedCatalogHasNoDiff(t *testing.T) {
	old, err := os.ReadFile("../internal/monitor/coins.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":%s}`, old)
	}))
	defer server.Close()
	path := newCatalogFile(t, old)
	if err := refresh(context.Background(), newClient(), server.URL, path); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, old)
}

func TestRefreshPreservesInvalidExistingCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not fetch when existing catalog is invalid")
	}))
	defer server.Close()
	cases := map[string][]byte{
		"noncanonical":                  []byte(`[{"id":"01","symbol":"LTC"}]`),
		"bom":                           append([]byte{0xef, 0xbb, 0xbf}, []byte(`[{"id":"1","symbol":"LTC"}]`)...),
		"utf16":                         {0xff, 0xfe, '[', 0, ']', 0},
		"invalid_json":                  []byte(`[{"id":"1","symbol":"LTC","extra":NaN}]`),
		"invalid_utf8":                  []byte("[{\"id\":\"1\",\"symbol\":\"LT\xff\"}]"),
		"duplicate_symbol":              []byte(`[{"id":"1","symbol":"LTC","symbol":null}]`),
		"duplicate_escaped_key":         []byte(`[{"id":"1","symbol":"LTC","sym\u0062ol":null}]`),
		"duplicate_id_null":             []byte(`[{"id":"1","id":null,"symbol":"LTC"}]`),
		"unpaired_high":                 []byte(`[{"id":"1","symbol":"\ud800"}]`),
		"unpaired_low":                  []byte(`[{"id":"1","symbol":"\udc00"}]`),
		"unpaired_unknown_field":        []byte(`[{"id":"1","symbol":"LTC","extra":"\ud800"}]`),
		"unpaired_high_followed_by_bmp": []byte(`[{"id":"1","symbol":"\ud800\u0041"}]`),
		"duplicate_field_type":          []byte(`[{"id":2,"id":"1","symbol":"LTC"}]`),
		"empty":                         []byte(`[]`),
	}
	for name, old := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.Parse(old); err == nil {
				t.Fatal("runtime catalog parser accepted fixture")
			}
			path := newCatalogFile(t, old)
			if err := refresh(context.Background(), newClient(), server.URL, path); err == nil || !strings.Contains(err.Error(), "invalid existing catalog") {
				t.Fatalf("expected invalid catalog error, got %v", err)
			}
			assertFile(t, path, old)
		})
	}
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "coins.json")
		if err := refresh(context.Background(), newClient(), server.URL, path); err == nil || !strings.Contains(err.Error(), "read existing catalog") {
			t.Fatalf("expected missing catalog error, got %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("missing catalog was created or could not be inspected: %v", err)
		}
	})
}

func TestRefreshRejectsBadHTTPAndData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server_error", 503, "unavailable"},
		{"redirect", 302, ""},
		{"malformed", 200, `{"data":`},
		{"empty", 200, `{"data":[]}`},
		{"bad_symbol", 200, `{"data":[{"id":"1","symbol":""}]}`},
		{"invalid_utf8", 200, "{\"data\":[{\"id\":\"1\",\"symbol\":\"LTC\"},{\"id\":\"3\",\"symbol\":\"\xff\"}]}"},
		{"duplicate_symbol", 200, `{"data":[{"id":"1","symbol":"LTC","symbol":null}]}`},
		{"duplicate_escaped_key", 200, `{"data":[{"id":"1","symbol":"LTC","sym\u0062ol":null}]}`},
		{"duplicate_id_null", 200, `{"data":[{"id":"1","id":null,"symbol":"LTC"}]}`},
		{"duplicate_data", 200, `{"data":[{"id":"1","symbol":"LTC"}],"data":null}`},
		{"unpaired_high", 200, `{"data":[{"id":"1","symbol":"LTC"},{"id":"3","symbol":"\ud800"}]}`},
		{"unpaired_low", 200, `{"data":[{"id":"1","symbol":"LTC"},{"id":"3","symbol":"\udc00"}]}`},
		{"unpaired_unknown_field", 200, `{"data":[{"id":"1","symbol":"LTC","extra":"\ud800"}]}`},
		{"unpaired_high_followed_by_bmp", 200, `{"data":[{"id":"1","symbol":"LTC"},{"id":"3","symbol":"\ud800\u0041"}]}`},
		{"oversize", 200, strings.Repeat("x", maxBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 302 {
					w.Header().Set("Location", "/unexpected")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			old := []byte(`[{"id":"1","symbol":"LTC"}]`)
			path := newCatalogFile(t, old)
			if err := refresh(context.Background(), newClient(), server.URL, path); err == nil {
				t.Fatal("expected refresh error")
			}
			assertFile(t, path, old)
		})
	}
}

func TestRefreshAcceptsValidUnicodeReplacementCharacter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"1","symbol":"LTC"},{"id":"3","symbol":"�"},{"id":"4","symbol":"\ud83d\ude00"},{"id":"5","symbol":"\\ud800"}]}`)
	}))
	defer server.Close()
	path := newCatalogFile(t, []byte(`[{"id":"1","symbol":"LTC"}]`))
	if err := refresh(context.Background(), newClient(), server.URL, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.Parse(data)
	if err != nil || len(entries) != 4 || entries[1].Symbol != "�" || entries[2].Symbol != "😀" || entries[3].Symbol != `\ud800` {
		t.Fatalf("valid replacement character not preserved: entries=%v error=%v", entries, err)
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("snapshot changed: got %q, want %q", got, want)
	}
}

func newCatalogFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coins.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The wrapper executes this test binary as a separate process, exercising the
// same run function and exit codes as the production updater.
func TestWorkflowFixtureProcess(t *testing.T) {
	if os.Getenv("MARKETWATCH_WORKFLOW_FIXTURE") != "1" {
		return
	}
	os.Exit(run(context.Background(), newClient(), os.Getenv("MARKETWATCH_FIXTURE_URL"), os.Getenv("MARKETWATCH_FIXTURE_PATH"), os.Stderr))
}

func TestWorkflowRefreshOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		old           string
		wantExit      int
		wantReady     string
		wantSummary   string
		wantUnchanged bool
	}{
		{"symbol_change", 200, `{"data":[{"id":"1","symbol":"X"}]}`, `[{"id":"1","symbol":"LTC"}]`, 0, "ready=false\n", `1: "LTC" -> "X"`, true},
		{"success", 200, `{"data":[{"id":"1","symbol":"LTC"},{"id":"2","symbol":"DOGE"}]}`, `[{"id":"1","symbol":"LTC"}]`, 0, "ready=true\n", "", false},
		{"server_error", 503, "unavailable", `[{"id":"1","symbol":"LTC"}]`, 1, "", "", true},
		{"malformed_response", 200, `{"data":`, `[{"id":"1","symbol":"LTC"}]`, 1, "", "", true},
		{"invalid_existing_catalog", 200, `{"data":[{"id":"1","symbol":"LTC"}]}`, `[{"id":"01","symbol":"LTC"}]`, 1, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			path := newCatalogFile(t, []byte(tc.old))
			dir := t.TempDir()
			output := filepath.Join(dir, "output")
			summary := filepath.Join(dir, "summary")
			for _, file := range []string{output, summary} {
				if err := os.WriteFile(file, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(dir, "updater")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec \"$MARKETWATCH_FIXTURE_BINARY\" -test.run '^TestWorkflowFixtureProcess$'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "refresh-workflow.sh", helper)
			cmd.Env = append(os.Environ(), "MARKETWATCH_WORKFLOW_FIXTURE=1", "MARKETWATCH_FIXTURE_BINARY="+executable, "MARKETWATCH_FIXTURE_URL="+server.URL, "MARKETWATCH_FIXTURE_PATH="+path, "GITHUB_OUTPUT="+output, "GITHUB_STEP_SUMMARY="+summary)
			logs, err := cmd.CombinedOutput()
			exit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				exit = exitErr.ExitCode()
			}
			if exit != tc.wantExit {
				t.Fatalf("exit=%d want %d; logs: %s", exit, tc.wantExit, logs)
			}
			ready, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(ready) != tc.wantReady {
				t.Fatalf("output=%q want %q; logs: %s", ready, tc.wantReady, logs)
			}
			report, err := os.ReadFile(summary)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSummary != "" && !strings.Contains(string(report), tc.wantSummary) {
				t.Fatalf("summary=%q want %q", report, tc.wantSummary)
			}
			if tc.wantSummary == "" && len(report) != 0 {
				t.Fatalf("unexpected summary: %s", report)
			}
			if tc.wantUnchanged {
				assertFile(t, path, []byte(tc.old))
			} else {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				entries, err := catalog.Parse(data)
				if err != nil || len(entries) != 2 || entries[1].Symbol != "DOGE" {
					t.Fatalf("unexpected catalog %s: %v", data, err)
				}
			}
			t.Logf("exit=%d output=%q summary=%q logs=%q", exit, ready, report, logs)
		})
	}
}

func TestWorkflowDoesNotSuppressUnexpectedExit(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "crashed-updater")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "output")
	cmd := exec.Command("sh", "refresh-workflow.sh", helper)
	cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output, "GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"))
	logs, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %v; logs: %s", err, logs)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("unexpected workflow output: %v", err)
	}
}

func TestWorkflowFailsWhenOutputCannotBeWritten(t *testing.T) {
	for _, status := range []int{0, blockedExitCode} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir := t.TempDir()
			helper := filepath.Join(dir, "updater")
			if err := os.WriteFile(helper, []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", status)), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "refresh-workflow.sh", helper)
			cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+filepath.Join(dir, "missing", "output"), "GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"))
			if logs, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("workflow succeeded without recording its result; logs: %s", logs)
			}
		})
	}
}
