package monitor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (r *Runner) pingHealthchecks(ctx context.Context, base, event string) {
	warn := func(reason string) {
		r.Logger.Warn("healthchecks_ping_failed", "event", event, "reason", reason)
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		warn("invalid HEALTHCHECKS_PING_URL")
		return
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	if event != "success" {
		u.Path += "/" + event
		if u.RawPath != "" {
			u.RawPath += "/" + event
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		warn("create request failed")
		return
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		// Transport errors can contain the secret ping URL.
		warn("request failed or timed out")
		return
	}
	_, err = readBody(resp)
	if resp.StatusCode != http.StatusOK {
		r.Logger.Warn("healthchecks_ping_failed", "event", event, "status", resp.StatusCode)
	} else if err != nil {
		warn("read response failed")
	}
}
