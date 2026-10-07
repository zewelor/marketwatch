package monitor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const stateVersion = 2

type Pending struct {
	ObservedAt time.Time `json:"observed_at"`
	Message    string    `json:"message"`
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

func acquireLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state lock unavailable: %w", err)
	}
	return f, nil
}

func loadState(path string) (State, error) {
	empty := State{Version: stateVersion, Rules: map[string]*RuleState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	if err := uniqueJSON(data); err != nil {
		return State{}, fmt.Errorf("invalid state: %w", err)
	}
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
			if err := exactFields(pending, "observed_at", "message"); err != nil {
				return State{}, fmt.Errorf("rule %q: %w", id, err)
			}
			if p.ObservedAt.IsZero() || p.Message == "" {
				return State{}, fmt.Errorf("rule %q: invalid pending", id)
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

// JSON's default decoder accepts repeated keys. Persistence must not hide them.
func uniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || keys[s] {
					return errors.New("duplicate or invalid JSON key")
				}
				keys[s] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func saveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".marketwatch-state-*")
	if err != nil {
		return fmt.Errorf("create state temp: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
