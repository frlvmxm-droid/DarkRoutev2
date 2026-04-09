package aiadvisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

type disabledProvider struct{}

func (d disabledProvider) Analyze(_ context.Context, _ AnalyzeInput) (Recommendation, error) {
	return Recommendation{}, fmt.Errorf("ai advisor disabled")
}

type httpJSONProvider struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func (p *httpJSONProvider) Analyze(ctx context.Context, in AnalyzeInput) (Recommendation, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Recommendation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return Recommendation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return Recommendation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Recommendation{}, fmt.Errorf("advisor HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var rec Recommendation
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&rec); err != nil {
		return Recommendation{}, err
	}
	return rec, nil
}

func NewProvider(cfg config.AIAdvisorConfig) Provider {
	if !cfg.Enabled || cfg.Provider == "" || cfg.Provider == "disabled" {
		return disabledProvider{}
	}
	if cfg.Provider == "http_json" {
		apiKey := ""
		if cfg.APIKeyFile != "" {
			if b, err := os.ReadFile(cfg.APIKeyFile); err == nil {
				apiKey = strings.TrimSpace(string(b))
			}
		}
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 8 * time.Second
		}
		return &httpJSONProvider{
			endpoint: cfg.Endpoint,
			apiKey:   apiKey,
			client:   &http.Client{Timeout: timeout},
		}
	}
	return disabledProvider{}
}
