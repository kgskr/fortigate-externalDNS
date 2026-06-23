package fortigate

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/dns"
	"github.com/gilsu/fortigate-external-dns/internal/plan"
)

type Client struct {
	cfg        config.FortiGateConfig
	httpClient *http.Client
	logger     *slog.Logger
}

type fortiResponse struct {
	Results []fortiRecord `json:"results"`
}

type fortiRecord struct {
	ID            any    `json:"id,omitempty"`
	MKey          string `json:"q_origin_key,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Type          string `json:"type,omitempty"`
	IP            string `json:"ip,omitempty"`
	IPv6          string `json:"ipv6,omitempty"`
	CanonicalName string `json:"canonical-name,omitempty"`
	TTL           int64  `json:"ttl,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Status        string `json:"status,omitempty"`
}

func NewClient(cfg config.FortiGateConfig, logger *slog.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		logger: logger,
	}, nil
}

func (c *Client) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.recordsPath(""), nil)
	if err != nil {
		return nil, err
	}
	var response fortiResponse
	if err := c.doJSON(req, &response); err != nil {
		return nil, err
	}
	var endpoints []dns.Endpoint
	for _, record := range response.Results {
		endpoint := record.toEndpoint(c.cfg.Zone)
		if endpoint.DNSName != "" && endpoint.RecordType != "" {
			endpoints = append(endpoints, endpoint.Normalize())
		}
	}
	return endpoints, nil
}

func (c *Client) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	for _, operation := range operations {
		if operation.Type == plan.OperationConflict {
			c.loggerOrDefault().Warn("ownership conflict; skipping operation", "operation", operation.String())
			continue
		}
		if dryRun {
			c.loggerOrDefault().Info("dry-run planned operation", "operation", operation.String())
			continue
		}
		if err := c.applyOne(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) applyOne(ctx context.Context, operation plan.Operation) error {
	switch operation.Type {
	case plan.OperationCreate:
		body := endpointToRecord(operation.Desired)
		req, err := c.newRequest(ctx, http.MethodPost, c.recordsPath(""), body)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	case plan.OperationUpdate, plan.OperationDeactivate:
		id := operation.Current.ProviderID
		if id == "" {
			id = operation.Current.DNSName
		}
		body := endpointToRecord(operation.Desired)
		req, err := c.newRequest(ctx, http.MethodPut, c.recordsPath(id), body)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	case plan.OperationDelete:
		id := operation.Current.ProviderID
		if id == "" {
			id = operation.Current.DNSName
		}
		req, err := c.newRequest(ctx, http.MethodDelete, c.recordsPath(id), nil)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	default:
		return nil
	}
}

func (c *Client) newRequest(ctx context.Context, method, requestPath string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	endpoint, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	endpoint.Path = path.Join(endpoint.Path, requestPath)
	query := endpoint.Query()
	if c.cfg.VDOM != "" {
		query.Set("vdom", c.cfg.VDOM)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	return req, nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	var lastErr error
	attempts := c.cfg.Retries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return err
			}
			req.Body = body
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			raw, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if out != nil && len(bytes.TrimSpace(raw)) > 0 {
					return json.Unmarshal(raw, out)
				}
				return nil
			}
			lastErr = fmt.Errorf("fortigate API %s %s returned HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, sanitizeBody(raw))
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return lastErr
			}
		}
		if attempt < attempts {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
	}
	return lastErr
}

func (c *Client) recordsPath(recordID string) string {
	base := "/api/v2/cmdb/system/dns-database/" + url.PathEscape(c.cfg.Zone) + "/dns-entry"
	if recordID == "" {
		return base
	}
	return base + "/" + url.PathEscape(recordID)
}

func (c *Client) loggerOrDefault() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

func (r fortiRecord) toEndpoint(zone string) dns.Endpoint {
	recordType := strings.ToUpper(r.Type)
	var targets []string
	switch recordType {
	case dns.RecordA:
		targets = appendIfNotEmpty(targets, r.IP)
	case dns.RecordAAAA:
		targets = appendIfNotEmpty(targets, r.IPv6)
	case dns.RecordCNAME:
		targets = appendIfNotEmpty(targets, r.CanonicalName)
	default:
		targets = appendIfNotEmpty(targets, r.IP)
		targets = appendIfNotEmpty(targets, r.IPv6)
		targets = appendIfNotEmpty(targets, r.CanonicalName)
	}
	return dns.Endpoint{
		DNSName:    r.Hostname,
		RecordType: recordType,
		Targets:    targets,
		TTL:        r.TTL,
		Zone:       zone,
		OwnerID:    ownerFromComment(r.Comment),
		Source:     sourceFromComment(r.Comment),
		ProviderID: recordID(r),
		Disabled:   strings.EqualFold(r.Status, "disable") || strings.EqualFold(r.Status, "disabled"),
	}
}

func endpointToRecord(endpoint dns.Endpoint) fortiRecord {
	endpoint = endpoint.Normalize()
	record := fortiRecord{
		Hostname: endpoint.DNSName,
		Type:     endpoint.RecordType,
		TTL:      endpoint.TTL,
		Comment:  managedComment(endpoint),
		Status:   "enable",
	}
	if endpoint.Disabled {
		record.Status = "disable"
	}
	if len(endpoint.Targets) > 0 {
		switch endpoint.RecordType {
		case dns.RecordAAAA:
			record.IPv6 = endpoint.Targets[0]
		case dns.RecordCNAME:
			record.CanonicalName = endpoint.Targets[0]
		default:
			record.IP = endpoint.Targets[0]
		}
	}
	return record
}

func managedComment(endpoint dns.Endpoint) string {
	return fmt.Sprintf("managed-by=fortigate-external-dns owner-id=%s source=%s", endpoint.OwnerID, endpoint.Source.String())
}

func ownerFromComment(comment string) string {
	for _, field := range strings.Fields(comment) {
		if strings.HasPrefix(field, "owner-id=") {
			return strings.TrimPrefix(field, "owner-id=")
		}
	}
	return ""
}

func sourceFromComment(comment string) dns.SourceRef {
	for _, field := range strings.Fields(comment) {
		if !strings.HasPrefix(field, "source=") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(field, "source="), "/")
		if len(parts) == 3 {
			return dns.SourceRef{Kind: parts[0], Namespace: parts[1], Name: parts[2]}
		}
		if len(parts) == 2 {
			return dns.SourceRef{Kind: parts[0], Name: parts[1]}
		}
	}
	return dns.SourceRef{}
}

func recordID(record fortiRecord) string {
	if record.MKey != "" {
		return record.MKey
	}
	switch value := record.ID.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	default:
		return ""
	}
}

func appendIfNotEmpty(values []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return values
	}
	return append(values, value)
}

func sanitizeBody(raw []byte) string {
	text := string(raw)
	if len(text) > 400 {
		text = text[:400] + "..."
	}
	return text
}
