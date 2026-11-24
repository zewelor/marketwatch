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

	"go.yaml.in/yaml/v4"
)

const (
	changeMargin   = 0.5
	priceMargin    = 0.005
	thresholdLabel = "threshold"
)

// Condition identifies how a rule evaluates a market quote.
type Condition string

const (
	ConditionAbove          Condition = "above"
	ConditionBelow          Condition = "below"
	ConditionAbsoluteChange Condition = "absolute-change"
)

// Window identifies the lookback period for percentage-change rules.
type Window string

const (
	WindowNone Window = ""
	Window24h  Window = "24h"
	Window7d   Window = "7d"
)

type conditionSpec struct {
	requiresWindow bool
	margin         float64
	triggered      func(value, threshold float64) bool
	rearmed        func(value, threshold float64) bool
	detail         func(rule Rule, value float64) string
}

var conditionSpecs = map[Condition]conditionSpec{
	ConditionAbove: {
		margin:    priceMargin,
		triggered: func(value, threshold float64) bool { return value >= threshold },
		rearmed:   func(value, threshold float64) bool { return value < threshold-threshold*priceMargin },
		detail:    formatPriceDetail("≥"),
	},
	ConditionBelow: {
		margin:    priceMargin,
		triggered: func(value, threshold float64) bool { return value <= threshold },
		rearmed:   func(value, threshold float64) bool { return value > threshold+threshold*priceMargin },
		detail:    formatPriceDetail("≤"),
	},
	ConditionAbsoluteChange: {
		requiresWindow: true,
		margin:         changeMargin,
		triggered:      func(value, threshold float64) bool { return math.Abs(value) >= threshold },
		rearmed: func(value, threshold float64) bool {
			if threshold <= changeMargin {
				return value == 0
			}
			return math.Abs(value) < threshold-changeMargin
		},
		detail: formatChangeDetail,
	},
}

var windowQuotes = map[Window]func(quote) (float64, error){
	Window24h: func(q quote) (float64, error) { return q.day, q.dayErr },
	Window7d:  func(q quote) (float64, error) { return q.week, q.weekErr },
}

func formatPriceDetail(operator string) func(Rule, float64) string {
	return func(rule Rule, _ float64) string {
		return fmt.Sprintf("(%s %s %s)", thresholdLabel, operator, formatPrice(rule.Threshold))
	}
}

func formatChangeDetail(rule Rule, value float64) string {
	sign := "+"
	if value < 0 {
		sign = "−"
	}
	return fmt.Sprintf("· %s%g%% in %s (%s ±%g%%)", sign, math.Abs(value), rule.Window, thresholdLabel, rule.Threshold)
}

func formatPrice(value float64) string {
	amount := strconv.FormatFloat(value, 'f', -1, 64)
	whole, fraction, hasFraction := strings.Cut(amount, ".")
	var grouped strings.Builder
	for i := range len(whole) {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteByte(whole[i])
	}
	if hasFraction {
		grouped.WriteByte('.')
		grouped.WriteString(fraction)
	}
	return grouped.String()
}

type Rule struct {
	ID        string
	Coin      string `yaml:"coin"`
	Symbol    string
	Threshold float64 `yaml:"threshold"`
	Window    Window  `yaml:"window,omitempty"`
	Condition Condition
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
		symbol, err := coinSymbol(string(w.Coin))
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", index+1, err)
		}
		var condition Condition
		threshold := 0.0
		for _, field := range []struct {
			condition Condition
			node      yaml.Node
		}{
			{ConditionAbsoluteChange, w.Threshold},
			{ConditionAbove, w.Above},
			{ConditionBelow, w.Below},
		} {
			if field.node.Kind == 0 {
				continue
			}
			if condition != "" {
				return nil, fmt.Errorf("rule %d: specify exactly one of threshold, above or below", index+1)
			}
			condition = field.condition
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
		window := WindowNone
		if w.Window.Kind != 0 {
			if w.Window.Kind != yaml.ScalarNode || w.Window.ShortTag() != "!!str" {
				return nil, fmt.Errorf("rule %d: window must be text", index+1)
			}
			window = Window(w.Window.Value)
		}
		if conditionSpecs[condition].requiresWindow {
			if _, ok := windowQuotes[window]; !ok {
				return nil, fmt.Errorf("rule %d: window must be 24h or 7d", index+1)
			}
		} else if w.Window.Kind != 0 {
			return nil, fmt.Errorf("rule %d: price rule cannot have window", index+1)
		}
		rule := Rule{Coin: string(w.Coin), Symbol: symbol, Threshold: threshold, Window: window, Condition: condition}
		rule.ID = rule.fingerprint()
		if seen[rule.ID] {
			return nil, fmt.Errorf("rule %d: duplicate rule definition", index+1)
		}
		seen[rule.ID] = true
		rules = append(rules, rule)
	}
	return rules, nil
}
func (r Rule) spec() conditionSpec {
	spec, ok := conditionSpecs[r.Condition]
	if !ok {
		panic(fmt.Sprintf("unknown rule condition %q", r.Condition))
	}
	return spec
}
func (r Rule) fingerprint() string {
	// This explicit contract excludes order, YAML spelling and comments.
	data, _ := json.Marshal([]string{"CoinLore", "USD", r.Coin, string(r.Condition), strconv.FormatFloat(r.Threshold, 'g', -1, 64), string(r.Window), strconv.FormatFloat(r.spec().margin, 'g', -1, 64)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (r Rule) triggered(value float64) bool {
	return r.spec().triggered(value, r.Threshold)
}
func (r Rule) rearmed(value float64) bool {
	return r.spec().rearmed(value, r.Threshold)
}
func (r Rule) formatMessage(price, value float64) string {
	return fmt.Sprintf("%s %s USD %s", r.Symbol, formatPrice(price), r.spec().detail(r, value))
}
