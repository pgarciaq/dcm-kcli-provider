package kweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

var ErrKwebUnreachable = errors.New("kweb is unreachable")
var ErrConflict = errors.New("resource already exists")
var ErrNotFound = errors.New("resource not found in kweb")

type KwebError struct {
	StatusCode int
	Reason     string
}

func (e *KwebError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("kweb error %d: %s", e.StatusCode, e.Reason)
	}
	return fmt.Sprintf("kweb error %d", e.StatusCode)
}

type VMInfo struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	IP           string `json:"ip,omitempty"`
	ID           string `json:"id,omitempty"`
	NumCPUs      int    `json:"numcpus,omitempty"`
	Memory       int    `json:"memory,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Plan         string `json:"plan,omitempty"`
	Kube         string `json:"kube,omitempty"`
	KubeType     string `json:"kubetype,omitempty"`
	CreationDate string `json:"creationdate,omitempty"`
	User         string `json:"user,omitempty"`
}

type ClusterInfo struct {
	Name        string `json:"name"`
	Status      string `json:"status,omitempty"`
	Version     string `json:"version,omitempty"`
	ClusterType string `json:"type,omitempty"`
	Plan        string `json:"plan,omitempty"`
	VMs         string `json:"vms,omitempty"`
	Nodes       [][]string `json:"nodes,omitempty"`
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	limiter    *rate.Limiter
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
		limiter: rate.NewLimiter(rate.Limit(10), 20),
	}
}

func (c *Client) CreateVM(ctx context.Context, name, profile string, params map[string]interface{}) error {
	body := map[string]interface{}{
		"name":    name,
		"profile": profile,
	}
	for k, v := range params {
		body[k] = v
	}
	return c.doPost(ctx, "/vms", body)
}

func (c *Client) ListVMs(ctx context.Context) ([]VMInfo, error) {
	data, err := c.doGet(ctx, "/vms")
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		VMs []VMInfo `json:"vms"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing vm list: %w", err)
	}
	return wrapper.VMs, nil
}

func (c *Client) GetVM(ctx context.Context, name string) (*VMInfo, error) {
	data, err := c.doGet(ctx, "/vms/"+name)
	if err != nil {
		return nil, err
	}

	var vm VMInfo
	if err := json.Unmarshal(data, &vm); err != nil {
		return nil, fmt.Errorf("parsing vm info: %w", err)
	}
	if vm.Name == "" {
		return nil, ErrNotFound
	}
	return &vm, nil
}

func (c *Client) DeleteVM(ctx context.Context, name string) error {
	return c.doDelete(ctx, "/vms/"+name)
}

func (c *Client) ListProfiles(ctx context.Context) ([]string, error) {
	data, err := c.doGet(ctx, "/vmprofiles")
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Profiles map[string]interface{} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing profiles: %w", err)
	}
	profiles := make([]string, 0, len(wrapper.Profiles))
	for name := range wrapper.Profiles {
		profiles = append(profiles, name)
	}
	return profiles, nil
}

func (c *Client) CreateCluster(ctx context.Context, name, clusterType string, params map[string]interface{}) error {
	body := map[string]interface{}{
		"cluster": name,
		"kubetype": clusterType,
	}
	for k, v := range params {
		body[k] = v
	}
	return c.doPost(ctx, "/kubes", body)
}

func (c *Client) ListClusters(ctx context.Context) ([]ClusterInfo, error) {
	data, err := c.doGet(ctx, "/kubes")
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Kubes map[string]json.RawMessage `json:"kubes"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing cluster list: %w", err)
	}

	clusters := make([]ClusterInfo, 0, len(wrapper.Kubes))
	for name, raw := range wrapper.Kubes {
		cl := ClusterInfo{Name: name}
		var info struct {
			Type string `json:"type"`
			Plan string `json:"plan"`
			VMs  string `json:"vms"`
		}
		if err := json.Unmarshal(raw, &info); err == nil {
			cl.ClusterType = info.Type
			cl.Plan = info.Plan
			cl.VMs = info.VMs
		}
		clusters = append(clusters, cl)
	}
	return clusters, nil
}

func (c *Client) GetCluster(ctx context.Context, name string) (*ClusterInfo, error) {
	data, err := c.doGet(ctx, "/kubes/"+name)
	if err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing cluster info: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrNotFound
	}

	cl := &ClusterInfo{Name: name}
	if v, ok := raw["version"]; ok {
		var version string
		if err := json.Unmarshal(v, &version); err == nil {
			cl.Version = version
		}
	}
	if n, ok := raw["nodes"]; ok {
		var nodes [][]string
		if err := json.Unmarshal(n, &nodes); err == nil {
			cl.Nodes = nodes
		}
	}
	if len(cl.Nodes) > 0 {
		cl.Status = "active"
	}
	return cl, nil
}

func (c *Client) DeleteCluster(ctx context.Context, name string) error {
	return c.doDelete(ctx, "/kubes/"+name)
}

func (c *Client) CheckHealth(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.limiter.Wait(ctx); err != nil {
		return false, fmt.Errorf("rate limiter: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/host", nil)
	if err != nil {
		return false, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, ErrKwebUnreachable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	return false, nil
}

func (c *Client) doPost(ctx context.Context, path string, body interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ErrKwebUnreachable
	}
	defer resp.Body.Close()

	return c.parseResponse(resp)
}

func (c *Client) doGet(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, ErrKwebUnreachable
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, parseErrorBody(resp.StatusCode, data)
	}
	return data, nil
}

func (c *Client) doDelete(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}

	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ErrKwebUnreachable
	}
	defer resp.Body.Close()

	return c.parseResponse(resp)
}

func (c *Client) parseResponse(resp *http.Response) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var structured map[string]interface{}
		if err := json.Unmarshal(data, &structured); err == nil {
			if result, ok := structured["result"].(string); ok && result == "failure" {
				kErr := &KwebError{StatusCode: resp.StatusCode}
				if reason, ok := structured["reason"].(string); ok {
					kErr.Reason = reason
				}
				return kErr
			}
		}
		return nil
	}

	return parseErrorBody(resp.StatusCode, data)
}

func parseErrorBody(statusCode int, data []byte) error {
	kErr := &KwebError{StatusCode: statusCode}

	var structured map[string]interface{}
	if err := json.Unmarshal(data, &structured); err == nil {
		if reason, ok := structured["reason"].(string); ok && reason != "" {
			kErr.Reason = reason
			if strings.Contains(strings.ToLower(reason), "already exists") ||
				strings.Contains(strings.ToLower(reason), "conflict") {
				return ErrConflict
			}
			return kErr
		}
		if result, ok := structured["result"].(string); ok && result == "failure" {
			if reason, ok := structured["reason"].(string); ok {
				kErr.Reason = reason
			}
			return kErr
		}
	}

	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "{}" {
		return kErr
	}

	if strings.Contains(raw, "<html") || strings.Contains(raw, "<!DOCTYPE") {
		kErr.Reason = fmt.Sprintf("kweb returned HTML error (HTTP %d)", statusCode)
		return kErr
	}

	kErr.Reason = raw
	return kErr
}
