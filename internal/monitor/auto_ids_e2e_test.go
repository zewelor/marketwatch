package monitor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These hashes are fixed examples of the pre-existing fingerprint contract.
const (
	btcAbove86000 = "1318459458cc349161e444e3b0ee6b291ae45bb6034476435dc3bd125c45978f"
	btcAbove90000 = "89a44fe8348fdafeb031e0ddf4713c8b85f02615aa791ddd6a346c1c727eb1da"
	btcBelow80000 = "f4f223da02e3f1f8a484ed6962db297417b43ac16ada9b28dc8ba7ca56052599"
	btcMovement3  = "b36b9468f1814cac5a4297c90155b4ddef36e22d20727ef9458e0e76dc2a56a1"
)

func TestE2EAutomaticRuleFingerprints(t *testing.T) {
	h := newHarness(t, `rules:
  - {coin: btc, above: 86000}
  - {coin: btc, above: 90000}
  - {coin: btc, below: 80000}
  - {coin: btc, threshold: 3, window: 24h}
`)
	h.prices("86000", "3", "0", "50", "0", "0")
	h.run(false)
	h.sent(2)
	original := h.state()
	for _, id := range []string{btcAbove86000, btcAbove90000, btcBelow80000, btcMovement3} {
		if r := original.Rules[id]; r == nil || r.Fingerprint != id {
			t.Fatalf("missing expected canonical fingerprint %s", id)
		}
	}
	if len(original.Rules) != 4 || h.ids != "90" {
		t.Fatal("multiple conditions were not independent or coin fetch was not deduplicated")
	}
	if strings.Contains(h.messages[0].Get("message"), btcAbove86000) {
		t.Fatal("internal fingerprint leaked into the notification text")
	}

	// Restart and reorder, changing only YAML spelling and coin aliases.
	previous := h.runner
	h.runner = New(previous.Logger)
	h.runner.Now = previous.Now
	h.runner.coinURL, h.runner.pushURL = previous.coinURL, previous.pushURL
	h.config(`rules:
  - {coin: "00090", threshold: 3.0, window: 24h}
  - {coin: BTC, below: 80000.0}
  - {coin: "90", above: 90000.0}
  - {coin: BtC, above: 86000.0}
`)
	h.run(false)
	h.sent(2)
	if !reflect.DeepEqual(original, h.state()) {
		t.Fatal("restart, order or equivalent YAML spelling changed rule state")
	}

	h.prices("90000", "0", "0", "50", "0", "0")
	h.run(false)
	h.sent(3)
	beforeChange := h.state()
	if !beforeChange.Rules[btcAbove90000].Active || beforeChange.Rules[btcMovement3].Active {
		t.Fatal("higher price threshold or percentage rearm was not independent")
	}
	h.config(`rules:
  - {coin: btc, above: 87000}
  - {coin: btc, above: 90000}
  - {coin: btc, below: 80000}
  - {coin: btc, threshold: 3, window: 24h}
`)
	h.run(false)
	h.sent(4)
	afterChange := h.state()
	if len(afterChange.Rules) != 4 || afterChange.Rules[btcAbove86000] != nil || !strings.Contains(h.logs.String(), "rule_removed") {
		t.Fatal("changed definition did not replace only its old key")
	}
	for _, id := range []string{btcAbove90000, btcBelow80000, btcMovement3} {
		if !reflect.DeepEqual(beforeChange.Rules[id], afterChange.Rules[id]) {
			t.Fatalf("definition change affected unchanged rule %s", id)
		}
	}
}

func writeLegacyState(t *testing.T, h *harness, rules map[string]*RuleState) []byte {
	t.Helper()
	data, err := json.MarshalIndent(State{Version: 2, Rules: rules}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.options.StatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.options.StatePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestE2ELegacyRuleFingerprintMigration(t *testing.T) {
	h := newHarness(t, `rules:
  - {coin: btc, above: 86000}
  - {coin: btc, below: 80000}
`)
	pending := &Pending{ObservedAt: h.now.Add(-time.Hour), Message: "Original pending BTC alert from the previous configuration"}
	before := writeLegacyState(t, h, map[string]*RuleState{
		"btc-high": {Fingerprint: btcAbove86000, Active: true, Pending: pending},
		"btc-low":  {Fingerprint: btcBelow80000},
	})
	h.marketStatus = 503
	h.options.DryRun = true
	h.run(true)
	h.sent(0)
	afterPreview, err := os.ReadFile(h.options.StatePath)
	if err != nil || !bytes.Equal(before, afterPreview) || !strings.Contains(h.logs.String(), "would_send") {
		t.Fatal("migration preview wrote state or lost pending")
	}

	h.options.DryRun = false
	h.run(true) // A provider outage must not block delivery of migrated pending.
	h.sent(1)
	state := h.state()
	if len(state.Rules) != 2 || state.Rules["btc-high"] != nil || state.Rules["btc-low"] != nil || state.Rules[btcAbove86000] == nil || state.Rules[btcBelow80000] == nil {
		t.Fatal("legacy state keys were not replaced with fingerprints")
	}
	if !state.Rules[btcAbove86000].Active || state.Rules[btcAbove86000].Pending != nil || h.messages[0].Get("message") != pending.Message {
		t.Fatal("migration lost activity or changed the pending notification")
	}
	if !strings.Contains(h.logs.String(), "rule_id_migrated") || strings.Contains(h.logs.String(), "pending_lost") {
		t.Fatal("migration was not logged or discarded a pending message")
	}
	h.marketStatus = 200
	h.prices("86000", "0", "0", "50", "0", "0")
	h.run(false)
	h.sent(1)
}

func TestE2EAmbiguousLegacyFingerprints(t *testing.T) {
	for _, secondKey := range []string{"other-manual-id", btcAbove86000} {
		t.Run(secondKey, func(t *testing.T) {
			h := newHarness(t, "rules:\n  - {coin: btc, above: 86000}\n")
			before := writeLegacyState(t, h, map[string]*RuleState{
				"btc-high": {Fingerprint: btcAbove86000, Active: true, Pending: &Pending{ObservedAt: h.now, Message: "First pending message"}},
				secondKey:  {Fingerprint: btcAbove86000, Active: false, Pending: &Pending{ObservedAt: h.now, Message: "Second pending message"}},
			})
			err := h.run(true)
			h.sent(0)
			after, readErr := os.ReadFile(h.options.StatePath)
			if !strings.Contains(err.Error(), "ambiguous") || h.gets != 0 || readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("ambiguous legacy state used HTTP, was overwritten, or was not reported explicitly")
			}
		})
	}
}
