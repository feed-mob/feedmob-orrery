// Package runner is the agent side: it registers, fetches tasks, executes them,
// and streams logs back.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
)

// Client speaks the runner.v1 contract over Connect's HTTP/JSON shape.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient builds a client. token may be empty for Register.
func NewClient(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		// The timeout must clear the server's FetchTask hold, or every
		// long-poll would look like a network failure.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// Token returns the credential in use.
func (c *Client) Token() string { return c.token }

// SetToken swaps the credential, e.g. after registering.
func (c *Client) SetToken(t string) { c.token = t }

func call[Req any, Resp any](ctx context.Context, c *Client, method string, req *Req) (*Resp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.base + protocol.ServicePath + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnauthorized {
		// 401 means stop. Retrying with the same credential cannot succeed and
		// a runner that hammers on is worse than one that exits loudly.
		return nil, ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("%s: %s: %s", method, res.Status, strings.TrimSpace(string(snippet)))
	}
	var out Resp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", method, err)
	}
	return &out, nil
}

// ErrUnauthorized signals a credential the server will not accept.
var ErrUnauthorized = fmt.Errorf("unauthorized")

func (c *Client) Register(ctx context.Context, req *protocol.RegisterRequest) (*protocol.RegisterResponse, error) {
	return call[protocol.RegisterRequest, protocol.RegisterResponse](ctx, c, "Register", req)
}

func (c *Client) Declare(ctx context.Context, req *protocol.DeclareRequest) (*protocol.DeclareResponse, error) {
	return call[protocol.DeclareRequest, protocol.DeclareResponse](ctx, c, "Declare", req)
}

func (c *Client) FetchTask(ctx context.Context, req *protocol.FetchTaskRequest) (*protocol.FetchTaskResponse, error) {
	return call[protocol.FetchTaskRequest, protocol.FetchTaskResponse](ctx, c, "FetchTask", req)
}

func (c *Client) UpdateTask(ctx context.Context, req *protocol.UpdateTaskRequest) (*protocol.UpdateTaskResponse, error) {
	return call[protocol.UpdateTaskRequest, protocol.UpdateTaskResponse](ctx, c, "UpdateTask", req)
}

func (c *Client) UpdateLog(ctx context.Context, req *protocol.UpdateLogRequest) (*protocol.UpdateLogResponse, error) {
	return call[protocol.UpdateLogRequest, protocol.UpdateLogResponse](ctx, c, "UpdateLog", req)
}

// ArtifactSession registers this runner's artifact credential with the control
// plane and returns the URL job containers should upload to. An empty URL means
// the server keeps no shared store and this runner should serve its own — which
// is the single-runner deployment, and stays correct.
func (c *Client) ArtifactSession(ctx context.Context, token string) (string, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/artifacts/session", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("artifact session: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		// An older control plane that does not know the endpoint. Falling back
		// to runner-local artifacts is what it would have done anyway.
		return "", nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", fmt.Errorf("artifact session: %s: %s", res.Status, strings.TrimSpace(string(snippet)))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("artifact session: decode: %w", err)
	}
	return out.URL, nil
}
