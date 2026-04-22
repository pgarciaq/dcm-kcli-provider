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
)

var ErrKwebUnreachable = errors.New("kweb is unreachable")
var ErrConflict = errors.New("resource already exists")

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
	Name   string `json:"name"`
	Status string `json:"status"`
	IP     string `json:"ip,omitempty"`
}

type ClusterInfo struct {
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Version string `json:"version,omitempty"`
	Nodes   string `json:"nodes,omitempty"`
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
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

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		var list []VMInfo
		if err2 := json.Unmarshal(data, &list); err2 != nil {
			return nil, fmt.Errorf("parsing vm list: %w", err)
		}
		return list, nil
	}

	var vms []VMInfo
	for name, val := range result {
		vm := VMInfo{Name: name}
		if info, ok := val.(map[string]interface{}); ok {
			if s, ok := info["status"].(string); ok {
				vm.Status = s
			}
			if ip, ok := info["ip"].(string); ok {
				vm.IP = ip
			}
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

func (c *Client) GetVM(ctx context.Context, name string) (*VMInfo, error) {
	data, err := c.doGet(ctx, "/vms/"+name)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing vm info: %w", err)
	}

	vm := &VMInfo{Name: name}
	if s, ok := result["status"].(string); ok {
		vm.Status = s
	}
	if ip, ok := result["ip"].(string); ok {
		vm.IP = ip
	}
	return vm, nil
}

func (c *Client) DeleteVM(ctx context.Context, name string) error {
	return c.doDelete(ctx, "/vms/"+name)
}

func (c *Client) ListProfiles(ctx context.Context) ([]string, error) {
	data, err := c.doGet(ctx, "/vmprofiles")
	if err != nil {
		return nil, err
	}

	var profiles []string
	if err := json.Unmarshal(data, &profiles); err != nil {
		var profileMap map[string]interface{}
		if err2 := json.Unmarshal(data, &profileMap); err2 != nil {
			return nil, fmt.Errorf("parsing profiles: %w", err)
		}
		for name := range profileMap {
			profiles = append(profiles, name)
		}
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

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		var list []ClusterInfo
		if err2 := json.Unmarshal(data, &list); err2 != nil {
			return nil, fmt.Errorf("parsing cluster list: %w", err)
		}
		return list, nil
	}

	var clusters []ClusterInfo
	for name, val := range result {
		cl := ClusterInfo{Name: name}
		if info, ok := val.(map[string]interface{}); ok {
			if s, ok := info["status"].(string); ok {
				cl.Status = s
			}
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

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing cluster info: %w", err)
	}

	cl := &ClusterInfo{Name: name}
	if s, ok := result["status"].(string); ok {
		cl.Status = s
	}
	if v, ok := result["version"].(string); ok {
		cl.Version = v
	}
	return cl, nil
}

func (c *Client) DeleteCluster(ctx context.Context, name string) error {
	return c.doDelete(ctx, "/kubes/"+name)
}

func (c *Client) CheckHealth(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

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
	if raw != "" && raw != "{}" {
		kErr.Reason = raw
	}

	return kErr
}
