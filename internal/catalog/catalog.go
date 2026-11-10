package catalog

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

type Entry struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
}

func NormalizeID(id string) (string, error) {
	if id == "" || strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", errors.New("coin must contain only decimal digits")
	}
	id = strings.TrimLeft(id, "0")
	if id == "" {
		return "", errors.New("coin ID must be positive")
	}
	return id, nil
}

// Parse validates the same catalog that is embedded in the application.
func Parse(data []byte) ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if err := Validate(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Validate checks canonical IDs, unique IDs and nonempty symbols.
func Validate(entries []Entry) error {
	if len(entries) == 0 {
		return errors.New("empty catalog")
	}
	seen := make(map[string]bool, len(entries))
	for index, entry := range entries {
		id, err := NormalizeID(entry.ID)
		if err != nil || id != entry.ID {
			return fmt.Errorf("row %d has invalid ID %q", index, entry.ID)
		}
		if seen[id] {
			return fmt.Errorf("duplicate ID %s", id)
		}
		if strings.TrimSpace(entry.Symbol) == "" {
			return fmt.Errorf("row %d has empty symbol", index)
		}
		seen[id] = true
	}
	return nil
}
