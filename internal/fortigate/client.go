package fortigate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/dns"
	"github.com/kgskr/fortigate-external-dns/internal/plan"
)

// errMissingProviderID is returned when an operation that mutates an existing
// FortiGate dns-entry has no provider ID. A FortiGate dns-entry mkey is its
// integer id, never the hostname, so there is no safe fallback.
var errMissingProviderID = errors.New("missing FortiGate provider ID")

// OperationRecorder records the outcome of applied operations by type and
// result. *metrics.Metrics satisfies it. It is optional: a nil recorder simply
// disables apply-outcome metrics.
type OperationRecorder interface {
	RecordOperation(opType, result string)
}

type Client struct {
	cfg        config.FortiGateConfig
	httpClient *http.Client
	logger     *slog.Logger
	recorder   OperationRecorder
}

type fortiResponse struct {
	Results []fortiRecord `json:"results"`
}

// fortiEnvelope is the common FortiGate cmdb response wrapper. FortiGate can
// return HTTP 2xx while signalling failure in the body (for example
// status="error"), so the envelope must be inspected even on a 2xx response.
type fortiEnvelope struct {
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	Error      *int   `json:"error"`
	Message    string `json:"message"`
}

type fortiRecord struct {
	ID            any    `json:"id,omitempty"`
	MKey          string `json:"q_origin_key,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Type          string `json:"type,omitempty"`
	IP            string `json:"ip,omitempty"`
	IPv6          string `json:"ipv6,omitempty"`
	CanonicalName string `json:"canonical-name,omitempty"`
	// TTL uses omitempty. This is safe because every controller-managed record is
	// created with a validated positive TTL (config guarantees DefaultTTL > 0 and
	// annotation TTLs are 1..MaxTTL), so a managed record never carries TTL 0 into
	// a create/update/deactivate PUT.
	TTL     int64  `json:"ttl,omitempty"`
	Comment string `json:"comment,omitempty"`
	Status  string `json:"status,omitempty"`
}

func NewClient(cfg config.FortiGateConfig, logger *slog.Logger, recorder OperationRecorder) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
	}
	if strings.TrimSpace(cfg.CAFile) != "" {
		// The CA bundle replaces (not extends) the system roots: the FortiGate
		// device is this client's only peer, so trusting anything beyond its
		// issuing chain only widens the attack surface. Validate has already
		// confirmed the file reads and parses; a race with file removal here still
		// fails closed.
		data, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read FortiGate CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("FortiGate CA file %q contains no PEM certificates", cfg.CAFile)
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		logger:   logger,
		recorder: recorder,
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
	var errs []error
	var attempted, succeeded, failed, skipped, conflict int

	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			// Stop issuing further writes once the reconcile context is canceled
			// (shutdown or lost leadership); remaining ops reconcile next loop.
			// Only surface the cancellation when no real operation error was already
			// recorded — otherwise the joined error would make errors.Is(err,
			// context.Canceled) true and mask a genuine failure from the caller's
			// exit-code decision.
			if len(errs) == 0 {
				errs = append(errs, err)
			}
			break
		}
		if operation.Type == plan.OperationConflict {
			conflict++
			c.recordOperation(operation.Type, "conflict")
			c.loggerOrDefault().Warn("ownership conflict; skipping operation", "operation", operation.String())
			continue
		}
		if dryRun {
			skipped++
			c.recordOperation(operation.Type, "skipped")
			c.loggerOrDefault().Info("dry-run planned operation", "operation", operation.String())
			continue
		}
		attempted++
		if err := c.applyOne(ctx, operation); err != nil {
			failed++
			c.recordOperation(operation.Type, "failed")
			errs = append(errs, fmt.Errorf("%s: %w", operation.String(), err))
			c.loggerOrDefault().Error("apply operation failed", "operation", operation.String(), "error", err)
			continue
		}
		succeeded++
		c.recordOperation(operation.Type, "applied")
		c.loggerOrDefault().Info("apply operation succeeded", "operation", operation.String())
	}

	c.loggerOrDefault().Info("apply summary",
		"attempted", attempted, "succeeded", succeeded, "failed", failed,
		"skipped", skipped, "conflict", conflict, "dryRun", dryRun)
	return errors.Join(errs...)
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
	case plan.OperationUpdate, plan.OperationDeactivate, plan.OperationReplace:
		id := strings.TrimSpace(operation.Current.ProviderID)
		if id == "" {
			return fmt.Errorf("cannot %s %q: %w", operation.Type, operation.Current.DNSName, errMissingProviderID)
		}
		body := endpointToRecord(operation.Desired)
		req, err := c.newRequest(ctx, http.MethodPut, c.recordsPath(id), body)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	case plan.OperationDelete:
		id := strings.TrimSpace(operation.Current.ProviderID)
		if id == "" {
			return fmt.Errorf("cannot delete %q: %w", operation.Current.DNSName, errMissingProviderID)
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
	ctx := req.Context()
	// A create POST has no client-supplied idempotency key: a FortiGate dns-entry
	// mkey is server-assigned, so re-issuing the same POST after a lost response
	// (5xx body, dropped connection, read timeout) can create a SECOND entry for
	// the same record. Only methods that target a specific record id (GET/PUT/
	// DELETE) are safe to retry; a failed create is left for the next reconcile.
	retryable := req.Method != http.MethodPost
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
			if !retryable {
				return lastErr
			}
		} else {
			raw, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			switch {
			case readErr != nil || closeErr != nil:
				// A body read/close failure is transient, like a network error, so
				// route it through the same retryable gate rather than returning.
				lastErr = readErr
				if lastErr == nil {
					lastErr = closeErr
				}
				if !retryable {
					return lastErr
				}
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				envErr := checkEnvelope(req, raw)
				if envErr == nil {
					if out != nil && len(bytes.TrimSpace(raw)) > 0 {
						return json.Unmarshal(raw, out)
					}
					return nil
				}
				// FortiGate signalled failure in the body despite a 2xx status.
				// Retry only transient envelope failures, and only for retryable
				// methods, mirroring the real-HTTP-status retry rule.
				lastErr = envErr
				if !retryable || !envelopeRetryable(raw) {
					return lastErr
				}
			default:
				lastErr = fmt.Errorf("fortigate API %s %s returned HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, truncateBody(raw))
				if !retryable || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) {
					return lastErr
				}
			}
		}
		if attempt < attempts {
			backoff := time.Duration(attempt) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return lastErr
}

// envelopeRetryable reports whether a FortiGate error envelope returned on an
// HTTP 2xx response describes a transient failure (a 429 or 5xx-equivalent
// http_status) worth retrying, mirroring the real-HTTP-status retry rule.
func envelopeRetryable(raw []byte) bool {
	var env fortiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	return env.HTTPStatus == http.StatusTooManyRequests || env.HTTPStatus >= 500
}

// checkEnvelope rejects a FortiGate response whose body signals failure even
// though the HTTP status was 2xx. Bodies that are not a recognizable envelope
// (for example a bare list) are left for the caller to parse.
func checkEnvelope(req *http.Request, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var env fortiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if env.Status != "" && !strings.EqualFold(env.Status, "success") {
		return fmt.Errorf("fortigate API %s %s returned status %q (http_status=%d): %s",
			req.Method, req.URL.Path, env.Status, env.HTTPStatus, truncateBody(raw))
	}
	if env.HTTPStatus != 0 && (env.HTTPStatus < 200 || env.HTTPStatus >= 300) {
		return fmt.Errorf("fortigate API %s %s returned http_status %d: %s",
			req.Method, req.URL.Path, env.HTTPStatus, truncateBody(raw))
	}
	// A nonzero cmdb error code signals failure even when status/http_status look
	// benign (0 or absent means success).
	if env.Error != nil && *env.Error != 0 {
		return fmt.Errorf("fortigate API %s %s returned error code %d: %s",
			req.Method, req.URL.Path, *env.Error, truncateBody(raw))
	}
	return nil
}

func (c *Client) recordsPath(recordID string) string {
	base := "/api/v2/cmdb/system/dns-database/" + url.PathEscape(c.cfg.Zone) + "/dns-entry"
	if recordID == "" {
		return base
	}
	return base + "/" + url.PathEscape(recordID)
}

func (c *Client) recordOperation(opType, result string) {
	if c.recorder != nil {
		c.recorder.RecordOperation(opType, result)
	}
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
	return fmt.Sprintf("managed-by=fortigate-external-dns;owner-id=%s;source=%s", endpoint.OwnerID, endpoint.Source.String())
}

// parseCommentFields parses the managed-record comment into key/value pairs. The
// current format is semicolon-delimited so values such as an owner ID that
// contains spaces survive a round trip. Legacy space-delimited comments written
// by earlier versions are still understood for backward compatibility.
func parseCommentFields(comment string) map[string]string {
	segments := strings.Split(comment, ";")
	if len(segments) == 1 {
		segments = strings.Fields(comment)
	}
	fields := map[string]string{}
	for _, segment := range segments {
		key, value, ok := strings.Cut(strings.TrimSpace(segment), "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}

func ownerFromComment(comment string) string {
	return parseCommentFields(comment)["owner-id"]
}

func sourceFromComment(comment string) dns.SourceRef {
	value := parseCommentFields(comment)["source"]
	if value == "" {
		return dns.SourceRef{}
	}
	parts := strings.Split(value, "/")
	if len(parts) == 3 {
		return dns.SourceRef{Kind: parts[0], Namespace: parts[1], Name: parts[2]}
	}
	if len(parts) == 2 {
		return dns.SourceRef{Kind: parts[0], Name: parts[1]}
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

// truncateBody caps a FortiGate-returned response body for inclusion in an error
// message. No redaction is needed: the API token is only ever sent in the request
// Authorization header and is never echoed back in a response body.
func truncateBody(raw []byte) string {
	text := string(raw)
	if len(text) > 400 {
		text = text[:400] + "..."
	}
	return text
}
