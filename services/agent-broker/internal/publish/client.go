// Package publish: the validator's client for POST /v1/publish (spec 30, FR-33).
package publish

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/rules"
)

// Client posts publish requests to the publisher over mTLS.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a client with the validator's publisher TLS configuration.
func NewClient(baseURL string, cfg *tls.Config) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP: &http.Client{
			Timeout:   2 * time.Minute,
			Transport: &http.Transport{TLSClientConfig: cfg},
		},
	}
}

// Publish sends one request. A structured publisher rejection is returned as a
// *rules.Rejection so the validator can forward the rule id and status.
func (c *Client) Publish(ctx context.Context, req PublishRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("publish: encode request: %w", err)
	}

	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/publish"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("publish: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("publish: publisher request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	var rejected struct {
		RuleID string `json:"rule_id"`
		Reason string `json:"reason"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &rejected); err == nil && rejected.RuleID != "" {
		reason := strings.TrimSpace(rejected.Reason)
		if reason == "" {
			reason = "publisher rejected the request"
		}
		return rules.New(rejected.RuleID, reason)
	}

	return rules.New(rules.PublishUnavailable, "publisher returned an unknown response")
}
