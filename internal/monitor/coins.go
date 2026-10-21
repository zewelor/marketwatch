package monitor

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed coins.json
var coinCatalogData []byte

type coinCatalogEntry struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
}

var (
	coinCatalogOnce sync.Once
	coinCatalog     map[string][]string
	coinSymbols     map[string]string
	coinCatalogErr  error
)

func coinSymbol(id string) (string, error) {
	if _, err := loadCoinCatalog(); err != nil {
		return "", err
	}
	symbol, ok := coinSymbols[id]
	if !ok {
		return "", fmt.Errorf("unknown coin ID %q; refresh the embedded catalog with scripts/update-coins.py", id)
	}
	return symbol, nil
}

func resolveCoin(value string) (string, error) {
	id, err := normalizeID(value)
	if err == nil {
		return id, nil
	}
	if isDecimalID(value) {
		return "", err
	}

	catalog, err := loadCoinCatalog()
	if err != nil {
		return "", err
	}
	return resolveCoinFromCatalog(value, catalog)
}

func resolveCoinFromCatalog(value string, catalog map[string][]string) (string, error) {
	ids := catalog[strings.ToUpper(value)]
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("unknown coin symbol %q; refresh the embedded catalog with scripts/update-coins.py", value)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("ambiguous coin symbol %q; CoinLore IDs: %s", value, strings.Join(sortedNumericIDs(ids), ", "))
	}
}

func loadCoinCatalog() (map[string][]string, error) {
	coinCatalogOnce.Do(func() {
		var entries []coinCatalogEntry
		if err := json.Unmarshal(coinCatalogData, &entries); err != nil {
			coinCatalogErr = fmt.Errorf("invalid embedded CoinLore catalog: %w", err)
			return
		}
		if len(entries) == 0 {
			coinCatalogErr = errors.New("invalid embedded CoinLore catalog: empty catalog")
			return
		}

		bySymbol := make(map[string][]string)
		byID := make(map[string]string, len(entries))
		for index, entry := range entries {
			id, err := normalizeID(entry.ID)
			if err != nil || id != entry.ID {
				coinCatalogErr = fmt.Errorf("invalid embedded CoinLore catalog: row %d has invalid ID", index)
				return
			}
			if _, exists := byID[id]; exists {
				coinCatalogErr = fmt.Errorf("invalid embedded CoinLore catalog: duplicate ID %s", id)
				return
			}
			if strings.TrimSpace(entry.Symbol) == "" {
				coinCatalogErr = fmt.Errorf("invalid embedded CoinLore catalog: row %d has empty symbol", index)
				return
			}
			key := strings.ToUpper(entry.Symbol)
			bySymbol[key] = append(bySymbol[key], id)
			byID[id] = key
		}
		for key := range bySymbol {
			bySymbol[key] = sortedNumericIDs(bySymbol[key])
		}
		coinCatalog = bySymbol
		coinSymbols = byID
	})
	return coinCatalog, coinCatalogErr
}

func isDecimalID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sortedNumericIDs(ids []string) []string {
	sorted := append([]string(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i]) != len(sorted[j]) {
			return len(sorted[i]) < len(sorted[j])
		}
		return sorted[i] < sorted[j]
	})
	return sorted
}
