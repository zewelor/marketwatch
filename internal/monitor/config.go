package monitor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const changeMargin = 0.5
const priceMargin = 0.005

type Rule struct {
	ID        string
	Coin      string  `yaml:"coin"`
	Threshold float64 `yaml:"threshold"`
	Window    string  `yaml:"window,omitempty"`
	Condition string
}

// YAML normally coerces numbers to strings. Coin selectors require text.
type coinID string

func (id *coinID) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		return errors.New("coin must be a symbol or quoted numeric ID")
	}
	canonical, err := resolveCoin(n.Value)
	if err != nil {
		return err
	}
	*id = coinID(canonical)
	return nil
}
func normalizeID(id string) (string, error) {
	if id == "" || strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", errors.New("coin must contain only decimal digits")
	}
	// No arbitrary numeric size limit: the API confirms existence.
	id = strings.TrimLeft(id, "0")
	if id == "" {
		return "", errors.New("coin ID must be positive")
	}
	return id, nil
}
func loadConfig(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var wire struct {
		Rules []struct {
			Coin      coinID    `yaml:"coin"`
			Threshold yaml.Node `yaml:"threshold"`
			Above     yaml.Node `yaml:"above"`
			Below     yaml.Node `yaml:"below"`
			Window    yaml.Node `yaml:"window"`
		} `yaml:"rules"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must contain exactly one YAML document")
	}
	if len(wire.Rules) == 0 {
		return nil, errors.New("config requires a nonempty rules list")
	}
	seen := map[string]bool{}
	rules := make([]Rule, 0, len(wire.Rules))
	for index, w := range wire.Rules {
		if w.Coin == "" {
			return nil, fmt.Errorf("rule %d: missing coin", index+1)
		}
		condition := ""
		threshold := 0.0
		for _, field := range []struct {
			name string
			node yaml.Node
		}{{"absolute-change", w.Threshold}, {"above", w.Above}, {"below", w.Below}} {
			if field.node.Kind == 0 {
				continue
			}
			if condition != "" {
				return nil, fmt.Errorf("rule %d: specify exactly one of threshold, above or below", index+1)
			}
			condition = field.name
			if field.node.Kind != yaml.ScalarNode || (field.node.ShortTag() != "!!int" && field.node.ShortTag() != "!!float") {
				return nil, fmt.Errorf("rule %d: condition must be a positive finite number", index+1)
			}
			if err := field.node.Decode(&threshold); err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 {
				return nil, fmt.Errorf("rule %d: condition must be a positive finite number", index+1)
			}
		}
		if condition == "" {
			return nil, fmt.Errorf("rule %d: specify exactly one of threshold, above or below", index+1)
		}
		window := ""
		if w.Window.Kind != 0 {
			if w.Window.Kind != yaml.ScalarNode || w.Window.ShortTag() != "!!str" {
				return nil, fmt.Errorf("rule %d: window must be text", index+1)
			}
			window = w.Window.Value
		}
		if condition == "absolute-change" {
			if window != "24h" && window != "7d" {
				return nil, fmt.Errorf("rule %d: window must be 24h or 7d", index+1)
			}
		} else if w.Window.Kind != 0 {
			return nil, fmt.Errorf("rule %d: price rule cannot have window", index+1)
		}
		rule := Rule{Coin: string(w.Coin), Threshold: threshold, Window: window, Condition: condition}
		rule.ID = rule.fingerprint()
		if seen[rule.ID] {
			return nil, fmt.Errorf("rule %d: duplicate rule definition", index+1)
		}
		seen[rule.ID] = true
		rules = append(rules, rule)
	}
	return rules, nil
}
func (r Rule) fingerprint() string {
	// This explicit contract excludes order, YAML spelling and comments.
	margin := changeMargin
	if r.Condition != "absolute-change" {
		margin = priceMargin
	}
	data, _ := json.Marshal([]string{"CoinLore", "USD", r.Coin, r.Condition, strconv.FormatFloat(r.Threshold, 'g', -1, 64), r.Window, strconv.FormatFloat(margin, 'g', -1, 64)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (r Rule) triggered(value float64) bool {
	switch r.Condition {
	case "above":
		return value >= r.Threshold
	case "below":
		return value <= r.Threshold
	}
	return math.Abs(value) >= r.Threshold
}
func (r Rule) rearmed(value float64) bool {
	switch r.Condition {
	case "above":
		return value < r.Threshold-r.Threshold*priceMargin
	case "below":
		return value > r.Threshold+r.Threshold*priceMargin
	}
	if r.Threshold <= changeMargin {
		return value == 0
	}
	return math.Abs(value) < r.Threshold-changeMargin
}
