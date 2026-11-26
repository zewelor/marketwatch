package monitor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const stateVersion = 2

type Pending struct {
	ObservedAt time.Time `json:"observed_at"`
	Title      string    `json:"title,omitempty"`
	Message    string    `json:"message"`
	GroupID    string    `json:"group_id,omitempty"`
}
type RuleState struct {
	Fingerprint string   `json:"fingerprint"`
	Active      bool     `json:"active"`
	Pending     *Pending `json:"pending,omitempty"`
}
type State struct {
	Version int                   `json:"version"`
	Rules   map[string]*RuleState `json:"rules"`
}

type pendingDelivery struct {
	*Pending
	ruleIDs []string
}

func (p *Pending) title() string {
	if p.Title == "" {
		return "Marketwatch"
	}
	return p.Title
}

func (s State) pendingDeliveries(rules []Rule) []pendingDelivery {
	var deliveries []pendingDelivery
	index := map[*Pending]int{}
	for _, rule := range rules {
		pending := s.Rules[rule.ID].Pending
		if pending == nil {
			continue
		}
		i, found := index[pending]
		if !found {
			i = len(deliveries)
			index[pending] = i
			deliveries = append(deliveries, pendingDelivery{Pending: pending})
		}
		deliveries[i].ruleIDs = append(deliveries[i].ruleIDs, rule.ID)
	}
	return deliveries
}

func (d pendingDelivery) clear(s State) {
	for _, id := range d.ruleIDs {
		s.Rules[id].Pending = nil
	}
}

func emptyState() State {
	return State{Version: stateVersion, Rules: map[string]*RuleState{}}
}

func decodeState(data []byte) (State, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return State{}, fmt.Errorf("invalid state object: %w", err)
	}
	if err := exactFields(root, "version", "rules"); err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("invalid state: %w", err)
	}
	if s.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported state version %d (expected %d); state left unchanged", s.Version, stateVersion)
	}
	if s.Rules == nil {
		return State{}, errors.New("incomplete state format: missing rules")
	}
	var raw struct {
		Rules map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return State{}, fmt.Errorf("invalid state: %w", err)
	}
	groups := map[string]*Pending{}
	for id, r := range s.Rules {
		if id == "" || r == nil {
			return State{}, errors.New("invalid state rule")
		}
		if err := exactFields(raw.Rules[id], "fingerprint", "active", "pending"); err != nil {
			return State{}, fmt.Errorf("rule %q: %w", id, err)
		}
		digest, err := hex.DecodeString(r.Fingerprint)
		if err != nil || len(digest) != 32 {
			return State{}, fmt.Errorf("rule %q: invalid fingerprint", id)
		}
		if value, ok := raw.Rules[id]["active"]; !ok || bytes.Equal(value, []byte("null")) {
			return State{}, fmt.Errorf("rule %q: missing active", id)
		}
		if p := r.Pending; p != nil {
			var pending map[string]json.RawMessage
			if err := json.Unmarshal(raw.Rules[id]["pending"], &pending); err != nil {
				return State{}, err
			}
			if err := exactFields(pending, "observed_at", "message", "title", "group_id"); err != nil {
				return State{}, fmt.Errorf("rule %q: %w", id, err)
			}
			_, hasTitle := pending["title"]
			_, hasGroupID := pending["group_id"]
			grouped := hasTitle || hasGroupID
			if p.ObservedAt.IsZero() || p.Message == "" || (grouped && (p.Title == "" || p.GroupID == "")) {
				return State{}, fmt.Errorf("rule %q: invalid pending", id)
			}
			if grouped {
				// Restore sharing among rule slots after decoding persisted copies.
				if group := groups[p.GroupID]; group != nil {
					if !group.ObservedAt.Equal(p.ObservedAt) || group.Title != p.Title || group.Message != p.Message {
						return State{}, fmt.Errorf("rule %q: inconsistent pending group", id)
					}
					r.Pending = group
				} else {
					groups[p.GroupID] = p
				}
			}
		}
	}
	return s, nil
}

// encoding/json accepts case-insensitive struct keys even in strict mode.
// Persisted schemas require their exact spelling to reject ambiguous state.
func exactFields(object map[string]json.RawMessage, allowed ...string) error {
	for field := range object {
		known := false
		for _, key := range allowed {
			if field == key {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown state field %q", field)
		}
	}
	return nil
}
