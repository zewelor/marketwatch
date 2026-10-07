package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const pendingLifetime = 3 * time.Hour

type Options struct {
	ConfigPath, StatePath string
	DryRun                bool
	Token, User           string
}
type Runner struct {
	Client           *http.Client
	Now              func() time.Time
	Logger           *slog.Logger
	coinURL, pushURL string
}

func New(logger *slog.Logger) *Runner {
	return &Runner{
		Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Now:    time.Now, Logger: logger, coinURL: "https://api.coinlore.net/api/ticker/", pushURL: "https://api.pushover.net/1/messages.json",
	}
}
func (r *Runner) Run(ctx context.Context, opts Options) (result error) {
	start := time.Now()
	evaluated, sent, created, expired := 0, 0, 0, 0
	defer func() {
		r.Logger.Info("summary", "evaluated", evaluated, "pending_created", created, "accepted", sent, "expired", expired, "failed", result != nil, "duration_ms", time.Since(start).Milliseconds(), "dry_run", opts.DryRun)
	}()
	rules, err := loadConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	if !opts.DryRun && (strings.TrimSpace(opts.Token) == "" || strings.TrimSpace(opts.User) == "") {
		return errors.New("PUSHOVER_APP_TOKEN and PUSHOVER_USER_KEY are required")
	}
	if !opts.DryRun {
		lock, err := acquireLock(opts.StatePath)
		if err != nil {
			return err
		}
		// Closing releases flock; never unlink the stable lock inode.
		defer lock.Close()
	}
	state, err := loadState(opts.StatePath)
	if err != nil {
		return err
	}
	r.Logger.Info("check_started", "source", coinLore.Name, "source_url", coinLore.URL, "currency", "USD", "rules", len(rules), "dry_run", opts.DryRun)
	present := map[string]bool{}
	for _, rule := range rules {
		present[rule.ID] = true
		old := state.Rules[rule.ID]
		if old == nil || old.Fingerprint != rule.fingerprint() {
			if old != nil {
				r.Logger.Warn("rule_reset", "rule_id", rule.ID, "pending_lost", old.Pending != nil)
			}
			state.Rules[rule.ID] = &RuleState{Fingerprint: rule.fingerprint()}
		}
	}
	for id, old := range state.Rules {
		if !present[id] {
			r.Logger.Warn("rule_removed", "rule_id", id, "pending_lost", old.Pending != nil)
			delete(state.Rules, id)
		}
	}
	var failures []error
	for _, rule := range rules {
		rs := state.Rules[rule.ID]
		if rs.Pending != nil && r.Now().Sub(rs.Pending.ObservedAt) >= pendingLifetime {
			r.Logger.Warn("pending_expired", "rule_id", rule.ID, "observed_at", rs.Pending.ObservedAt)
			rs.Pending = nil
			expired++
			failures = append(failures, fmt.Errorf("rule %q: pending expired without delivery", rule.ID))
		}
	}
	quotes, fetchErr := r.fetch(ctx, rules)
	if fetchErr != nil {
		r.Logger.Error("market_unavailable", "reason", fetchErr.Error())
		failures = append(failures, fetchErr)
	}
	for _, rule := range rules {
		if fetchErr != nil {
			continue
		}
		q, ok := quotes[rule.Coin]
		var dataErr error
		value := q.price
		if !ok {
			dataErr = errors.New("missing CoinLore ID")
		} else if q.priceErr != nil {
			dataErr = q.priceErr
		} else {
			switch rule.Window {
			case "24h":
				value = q.day
				dataErr = q.dayErr
			case "7d":
				value = q.week
				dataErr = q.weekErr
			}
		}
		if dataErr != nil {
			r.Logger.Error("rule_skipped", "rule_id", rule.ID, "coin", rule.Coin, "reason", dataErr.Error())
			failures = append(failures, fmt.Errorf("rule %q: %w", rule.ID, dataErr))
			continue
		}
		evaluated++
		rs := state.Rules[rule.ID]
		decision := "unchanged"
		if rs.Active {
			if rule.rearmed(value) {
				rs.Active = false
				decision = "rearmed"
			}
		} else if rule.triggered(value) {
			rs.Active = true
			decision = "alert"
			if rs.Pending != nil {
				r.Logger.Warn("pending_replaced", "rule_id", rule.ID, "lost_observed_at", rs.Pending.ObservedAt)
			}
			rs.Pending = render(rule, q)
			created++
		}
		r.Logger.Info("rule_evaluated", "source", coinLore.Name, "rule_id", rule.ID, "coin", rule.Coin, "currency", "USD", "price", q.price, "window", rule.Window, "value", value, "fetched_at", q.fetchedAt, "decision", decision, "active", rs.Active, "pending", rs.Pending != nil, "dry_run", opts.DryRun)
	}
	if opts.DryRun {
		for _, rule := range rules {
			if p := state.Rules[rule.ID].Pending; p != nil {
				r.Logger.Info("would_send", "rule_id", rule.ID, "observed_at", p.ObservedAt, "message", p.Message)
			}
		}
		return errors.Join(failures...)
	}
	if err := saveState(opts.StatePath, state); err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, rule := range rules {
		rs := state.Rules[rule.ID]
		if rs.Pending == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		// Network time can move an event across the expiry boundary.
		if r.Now().Sub(rs.Pending.ObservedAt) >= pendingLifetime {
			rs.Pending = nil
			expired++
			failures = append(failures, fmt.Errorf("rule %q: pending expired before send", rule.ID))
			r.Logger.Warn("pending_expired", "rule_id", rule.ID)
			if err := saveState(opts.StatePath, state); err != nil {
				return errors.Join(append(failures, err)...)
			}
			continue
		}
		stop, err := r.send(ctx, rs.Pending, opts)
		if err != nil {
			r.Logger.Error("send_failed", "rule_id", rule.ID, "reason", err.Error(), "remaining_sends_stopped", stop)
			failures = append(failures, fmt.Errorf("rule %q: %w", rule.ID, err))
			if stop {
				break
			}
			continue
		}
		now := r.Now().UTC()
		rs.Pending = nil
		sent++
		r.Logger.Info("pushover_accepted", "rule_id", rule.ID, "accepted_at", now)
		if err := saveState(opts.StatePath, state); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func render(rule Rule, q quote) *Pending {
	message := fmt.Sprintf("Rule %s: condition observed\nCoinLore ID %s; price %g USD\nCondition: ", rule.ID, rule.Coin, q.price)
	switch rule.Condition {
	case "above":
		message += fmt.Sprintf("price >= %g USD", rule.Threshold)
	case "below":
		message += fmt.Sprintf("price <= %g USD", rule.Threshold)
	default:
		change := q.day
		if rule.Window == "7d" {
			change = q.week
		}
		message += fmt.Sprintf("absolute change >= %g%% (%s); provider change: %g%%", rule.Threshold, rule.Window, change)
	}
	message += "\nData fetched: " + q.fetchedAt.Format(time.RFC3339Nano) + "\nFetch time does not confirm price freshness."
	return &Pending{ObservedAt: q.fetchedAt, Message: message}
}
