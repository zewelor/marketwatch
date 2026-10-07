package monitor

import (
	"strings"
	"testing"
)

func TestResolveCoin(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "bitcoin symbol", input: "btc", want: "90"},
		{name: "ethereum mixed case", input: "EtH", want: "80"},
		{name: "numeric ID canonicalized", input: "00090", want: "90"},
		{name: "zero ID rejected", input: "0", wantErr: "positive"},
		{name: "unknown symbol is actionable", input: "not-a-coin", wantErr: "scripts/update-coins.py"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCoin(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveCoin(%q) error = %v, want error containing %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCoin(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("resolveCoin(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveCoinAmbiguousSymbol(t *testing.T) {
	catalog := map[string][]string{"DUP": {"900", "80", "3"}}
	_, err := resolveCoinFromCatalog("DuP", catalog)
	if err == nil {
		t.Fatal("ambiguous symbol resolved without an error")
	}
	if got, want := err.Error(), `ambiguous coin symbol "DuP"; CoinLore IDs: 3, 80, 900`; got != want {
		t.Fatalf("ambiguity error = %q, want %q", got, want)
	}
}
