package main

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"marketwatch/internal/catalog"
)

const (
	assetsURL  = "https://api.coinlore.net/api/assets/"
	outputPath = "internal/monitor/coins.json"
	maxBytes   = 4 << 20
)

func main() {
	if err := refresh(context.Background(), newClient(), assetsURL, outputPath); err != nil {
		fmt.Fprintln(os.Stderr, "update CoinLore catalog failed:", err)
		os.Exit(1)
	}
}

func newClient() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func refresh(ctx context.Context, client *http.Client, url, path string) error {
	oldData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing catalog: %w", err)
	}
	old, err := catalog.Parse(oldData)
	if err != nil {
		return fmt.Errorf("invalid existing catalog: %w", err)
	}
	entries, err := fetchCatalog(ctx, client, url)
	if err != nil {
		return err
	}
	if err := checkSymbolChanges(old, entries); err != nil {
		return err
	}
	var result bytes.Buffer
	encoder := json.NewEncoder(&result)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(entries); err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	if err := writeAtomically(path, result.Bytes()); err != nil {
		return fmt.Errorf("write catalog: %w", err)
	}
	fmt.Printf("wrote %d CoinLore coins to %s\n", len(entries), path)
	return nil
}

func fetchCatalog(ctx context.Context, client *http.Client, url string) ([]catalog.Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "marketwatch coin catalog updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinLore HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxBytes {
		return nil, errors.New("CoinLore response exceeds size limit")
	}
	var payload struct {
		Data []catalog.Entry `json:"data"`
	}
	if err := jsonv2.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid CoinLore response: %w", err)
	}
	for i := range payload.Data {
		id, err := catalog.NormalizeID(payload.Data[i].ID)
		if err != nil {
			return nil, fmt.Errorf("CoinLore row %d: %w", i, err)
		}
		payload.Data[i].ID = id
	}
	if err := catalog.Validate(payload.Data); err != nil {
		return nil, fmt.Errorf("invalid CoinLore catalog: %w", err)
	}
	sort.Slice(payload.Data, func(i, j int) bool {
		a, b := payload.Data[i].ID, payload.Data[j].ID
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	return payload.Data, nil
}

func checkSymbolChanges(old, coins []catalog.Entry) error {
	oldSymbols := make(map[string]string, len(old))
	for _, coin := range old {
		oldSymbols[coin.ID] = coin.Symbol
	}
	var changes []string
	for _, coin := range coins {
		if symbol, ok := oldSymbols[coin.ID]; ok && symbol != coin.Symbol {
			changes = append(changes, fmt.Sprintf("%s: %q -> %q", coin.ID, symbol, coin.Symbol))
		}
	}
	if len(changes) > 0 {
		return fmt.Errorf("existing coin symbols changed; review CoinLore data: %s", strings.Join(changes, "; "))
	}
	return nil
}

func writeAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".coins.*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
