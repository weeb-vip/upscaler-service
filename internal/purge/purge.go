// Package purge drops replaced objects from Cloudflare's cache, so the CDN
// serves the new bytes and the resizer rebuilds its variants from them.
package purge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Purger invalidates URLs at the edge. A nil *Cloudflare purges nothing.
type Purger interface {
	Purge(ctx context.Context, urls []string) error
}

type Cloudflare struct {
	ZoneID string
	Token  string
	Client *http.Client
	// Endpoint is overridable for tests.
	Endpoint string
}

func NewCloudflare(zoneID, token string) *Cloudflare {
	if zoneID == "" || token == "" {
		return nil
	}
	return &Cloudflare{ZoneID: zoneID, Token: token, Client: &http.Client{Timeout: 20 * time.Second}, Endpoint: "https://api.cloudflare.com/client/v4"}
}

// Purge sends one purge_cache request. Cloudflare takes up to 30 URLs per
// call; callers here never send more than a handful.
func (c *Cloudflare) Purge(ctx context.Context, urls []string) error {
	if c == nil || len(urls) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"files": urls})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/zones/%s/purge_cache", c.Endpoint, c.ZoneID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("cloudflare purge: %s: %s", res.Status, msg)
	}
	return nil
}
