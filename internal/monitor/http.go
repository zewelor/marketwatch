package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponse = 4 << 20

var coinLore = struct {
	Name, URL string
}{
	Name: "CoinLore", URL: "https://www.coinlore.com",
}

type quote struct {
	price, day, week          float64
	priceErr, dayErr, weekErr error
	fetchedAt                 time.Time
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, errors.New("read HTTP response failed")
	}
	if len(body) > maxResponse {
		return nil, errors.New("HTTP response exceeds size limit")
	}
	return body, nil
}
func number(raw json.RawMessage, positive bool) (float64, error) {
	var text string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &text) != nil || text == "" {
		return 0, errors.New("missing or non-string number")
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || (positive && n <= 0) {
		return 0, errors.New("invalid numeric value")
	}
	return n, nil
}
func (r *Runner) fetch(ctx context.Context, rules []Rule) (map[string]quote, error) {
	ids := []string{}
	wanted := map[string]bool{}
	for _, rule := range rules {
		if !wanted[rule.Coin] {
			wanted[rule.Coin] = true
			ids = append(ids, rule.Coin)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.coinURL+"?id="+strings.Join(ids, ","), nil)
	if err != nil {
		return nil, errors.New("create CoinLore request failed")
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, errors.New("CoinLore request failed or cancelled")
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	fetched := r.Now().UTC()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CoinLore HTTP %d", resp.StatusCode)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, errors.New("invalid CoinLore JSON")
	}
	quotes := map[string]quote{}
	seen := map[string]bool{}
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			continue
		}
		id, err = normalizeID(id)
		if err != nil || !wanted[id] {
			continue
		}
		if seen[id] {
			quotes[id] = quote{priceErr: errors.New("duplicate CoinLore ID")}
			continue
		}
		seen[id] = true
		q := quote{fetchedAt: fetched}
		q.price, q.priceErr = number(row["price_usd"], true)
		q.day, q.dayErr = number(row["percent_change_24h"], false)
		q.week, q.weekErr = number(row["percent_change_7d"], false)
		quotes[id] = q
	}
	return quotes, nil
}

func (r *Runner) send(ctx context.Context, p *Pending, opts Options) (stop bool, err error) {
	form := url.Values{"token": {opts.Token}, "user": {opts.User}, "priority": {"0"}, "title": {"Marketwatch"}, "message": {p.Message}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.pushURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, errors.New("create Pushover request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.Client.Do(req)
	if err != nil {
		return false, errors.New("Pushover request failed or cancelled")
	}
	body, readErr := readBody(resp)
	stop = resp.StatusCode >= 400 && resp.StatusCode < 500
	if stop {
		return true, fmt.Errorf("Pushover HTTP %d: check credentials and quota", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Pushover HTTP %d", resp.StatusCode)
	}
	if readErr != nil {
		return false, readErr
	}
	var result struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Status != 1 {
		return false, errors.New("Pushover did not confirm status=1")
	}
	return false, nil
}
