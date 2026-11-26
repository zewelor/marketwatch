package monitor

import (
	"fmt"
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
	h.sent(1)
	original := h.state()
	for _, id := range []string{btcAbove86000, btcAbove90000, btcBelow80000, btcMovement3} {
		if original.Rules[id] == nil {
			t.Fatalf("missing expected canonical fingerprint %s", id)
		}
	}
	if len(original.Rules) != 4 || h.ids != "90" {
		t.Fatal("multiple conditions were not independent or coin fetch was not deduplicated")
	}
	if got := h.messages[0]; got.Get("title") != "BTC | 86,000 USD" || got.Get("message") != "24h +3%" {
		t.Fatalf("unexpected combined notification: %v", got)
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
	h.sent(1)
	if !reflect.DeepEqual(original, h.state()) {
		t.Fatal("restart, order or equivalent YAML spelling changed rule state")
	}

	h.prices("90000", "0", "0", "50", "0", "0")
	h.run(false)
	h.sent(2)
	if got := h.messages[1]; got.Get("title") != "BTC | 90,000 USD" || got.Get("message") != "Price alert" {
		t.Fatalf("unexpected price-only notification: %v", got)
	}
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
	h.sent(3)
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

func TestE2EStateVersion2RequiresReset(t *testing.T) {
	h := newHarness(t, "rules:\n  - {coin: btc, above: 86000}\n")
	h.prices("86500.25", "0", "0", "50", "0", "0")
	old := []byte(fmt.Sprintf(`{"version":2,"rules":{%q:{"fingerprint":%q,"active":true,"pending":{"observed_at":%q,"message":"BTC 86,500.25 USD (threshold ≥ 86,000)"}}}}`,
		btcAbove86000, btcAbove86000, h.now.Format(time.RFC3339)))
	setPostgresTestStateJSON(t, old)

	err := h.run(true)
	h.sent(0)
	after, exists := postgresTestStateJSON(t)
	if !strings.Contains(err.Error(), "unsupported state version 2 (expected 3)") ||
		h.gets != 0 || !exists || !postgresTestJSONEqual(t, old, after) {
		t.Fatalf("old state must fail before external HTTP and stay intact: %v", err)
	}

	resetPostgresTestState(t)
	h.run(false)
	h.sent(1)
	state := h.state()
	if state.Version != stateVersion || !state.Rules[btcAbove86000].Active {
		t.Fatal("fresh table did not initialize new state and record the alert")
	}
	if push := h.messages[0]; push.Get("title") != "BTC | 86,500.25 USD" || push.Get("message") != "Price alert" || push.Get("timestamp") == "" {
		t.Fatalf("unexpected notification after reset: %v", push)
	}
}
