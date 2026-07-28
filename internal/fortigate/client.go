package fortigate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
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

const fortiListPageSize = 1000

type fortiResponse struct {
	Results      []fortiRecord `json:"results"`
	LimitReached *bool         `json:"limit_reached"`
	MatchedCount *int          `json:"matched_count"`
	NextIndex    *int          `json:"next_idx"`
	Revision     *string       `json:"revision"`
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
	MKey          any    `json:"q_origin_key,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Type          string `json:"type,omitempty"`
	IP            string `json:"ip,omitempty"`
	IPv6          string `json:"ipv6,omitempty"`
	CanonicalName string `json:"canonical-name,omitempty"`
	// TTL uses omitempty. This is safe because every controller-managed record is
	// created with a validated positive TTL (config guarantees DefaultTTL > 0 and
	// annotation TTLs are 1..MaxTTL), so a managed record never carries TTL 0 into
	// a create/update/deactivate PUT.
	TTL    int64  `json:"ttl,omitempty"`
	Status string `json:"status,omitempty"`
}

func NewClient(cfg config.FortiGateConfig, logger *slog.Logger, recorder OperationRecorder) (*Client, error) {
	transportPolicy, err := newTransportPolicy(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:        cfg,
		httpClient: transportPolicy.client(cfg.Timeout),
		logger:     logger,
		recorder:   recorder,
	}, nil
}

func (c *Client) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	var records []fortiRecord
	seenProviderIDs := map[string]struct{}{}
	start := 0
	expectedMatchedCount := -1
	revision := ""
	paginationStarted := false

	for {
		req, err := c.newRequest(ctx, http.MethodGet, c.recordsPath(""), nil)
		if err != nil {
			return nil, err
		}
		query := req.URL.Query()
		query.Set("start", strconv.Itoa(start))
		query.Set("count", strconv.Itoa(fortiListPageSize))
		req.URL.RawQuery = query.Encode()

		var response fortiResponse
		if err := c.doJSON(req, &response); err != nil {
			return nil, err
		}
		if response.LimitReached == nil || response.MatchedCount == nil || response.NextIndex == nil || response.Revision == nil {
			return nil, errors.New("FortiGate list response is missing pagination metadata")
		}
		if *response.MatchedCount < 0 {
			return nil, fmt.Errorf("FortiGate list response has negative matched_count %d", *response.MatchedCount)
		}
		if expectedMatchedCount == -1 {
			expectedMatchedCount = *response.MatchedCount
		} else if *response.MatchedCount != expectedMatchedCount {
			return nil, fmt.Errorf("FortiGate list matched_count changed during pagination: %d to %d", expectedMatchedCount, *response.MatchedCount)
		}
		responseRevision := strings.TrimSpace(*response.Revision)
		if paginationStarted || *response.LimitReached {
			if responseRevision == "" {
				return nil, errors.New("FortiGate paginated list response is missing a non-empty revision")
			}
			if revision != "" && responseRevision != revision {
				return nil, fmt.Errorf("FortiGate list revision changed during pagination: %q to %q", revision, responseRevision)
			}
			revision = responseRevision
		} else if responseRevision != "" {
			revision = responseRevision
		}

		for _, record := range response.Results {
			providerID := strings.TrimSpace(recordID(record))
			if providerID == "" {
				return nil, errors.New("FortiGate list response contains a record without a provider ID")
			}
			if _, exists := seenProviderIDs[providerID]; exists {
				return nil, fmt.Errorf("FortiGate list response repeats provider ID %q", providerID)
			}
			seenProviderIDs[providerID] = struct{}{}
			records = append(records, record)
		}

		if !*response.LimitReached {
			if len(records) != expectedMatchedCount {
				return nil, fmt.Errorf("FortiGate list response is incomplete: collected %d of %d matched records", len(records), expectedMatchedCount)
			}
			break
		}
		if len(response.Results) == 0 {
			return nil, errors.New("FortiGate list pagination did not advance: limited response contained no records")
		}
		nextStart := *response.NextIndex + 1
		if nextStart <= start {
			return nil, fmt.Errorf("FortiGate list pagination did not advance: start=%d next_idx=%d", start, *response.NextIndex)
		}
		paginationStarted = true
		start = nextStart
	}

	var endpoints []dns.Endpoint
	for _, record := range records {
		endpoint := record.toEndpoint(c.cfg.Zone)
		if endpoint.DNSName != "" && endpoint.RecordType != "" {
			endpoints = append(endpoints, endpoint.Normalize())
		}
	}
	return endpoints, nil
}

// ListRecordsWithRevision returns a content-addressed revision for the complete
// normalized snapshot. FortiGate may omit its revision on a single-page list;
// a content digest gives plan approval the same fail-closed identity in both
// single- and multi-page cases.
func (c *Client) ListRecordsWithRevision(ctx context.Context) ([]dns.Endpoint, string, error) {
	records, err := c.ListRecords(ctx)
	if err != nil {
		return nil, "", err
	}
	revision, err := recordsRevision(records)
	if err != nil {
		return nil, "", err
	}
	return records, revision, nil
}

func recordsRevision(records []dns.Endpoint) (string, error) {
	type record struct {
		ProviderID string   `json:"providerID"`
		Zone       string   `json:"zone"`
		DNSName    string   `json:"dnsName"`
		RecordType string   `json:"recordType"`
		Targets    []string `json:"targets"`
		TTL        int64    `json:"ttl"`
		Disabled   bool     `json:"disabled"`
	}
	canonical := make([]record, 0, len(records))
	for _, endpoint := range records {
		endpoint = endpoint.Normalize()
		canonical = append(canonical, record{
			ProviderID: endpoint.ProviderID, Zone: endpoint.Zone, DNSName: endpoint.DNSName,
			RecordType: endpoint.RecordType, Targets: append([]string(nil), endpoint.Targets...),
			TTL: endpoint.TTL, Disabled: endpoint.Disabled,
		})
	}
	sort.Slice(canonical, func(i, j int) bool {
		left, _ := json.Marshal(canonical[i])
		right, _ := json.Marshal(canonical[j])
		return bytes.Compare(left, right) < 0
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("serialize FortiGate snapshot revision: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (c *Client) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	var errs []error
	var attempted, succeeded, failed, skipped, conflict int
	failedPrerequisiteGroups := map[string]struct{}{}

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
			c.loggerOrDefault().Warn("planning conflict; skipping operation", "operation", operation.String())
			continue
		}
		if dryRun {
			skipped++
			c.recordOperation(operation.Type, "skipped")
			c.loggerOrDefault().Info("dry-run planned operation", "operation", operation.String())
			continue
		}
		if isCleanupOperation(operation.Type) {
			if _, blocked := failedPrerequisiteGroups[operation.Current.MutationGroupKey()]; blocked {
				skipped++
				c.recordOperation(operation.Type, "skipped")
				c.loggerOrDefault().Warn("dependent cleanup skipped after failed prerequisite mutation", "operation", operation.String())
				continue
			}
		}
		attempted++
		if err := c.applyOne(ctx, operation); err != nil {
			failed++
			if isCleanupPrerequisite(operation.Type) {
				failedPrerequisiteGroups[operation.Desired.MutationGroupKey()] = struct{}{}
			}
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

func isCleanupOperation(operationType string) bool {
	return operationType == plan.OperationDelete || operationType == plan.OperationDeactivate
}

func isCleanupPrerequisite(operationType string) bool {
	return operationType == plan.OperationCreate || operationType == plan.OperationUpdate || operationType == plan.OperationReplace
}

func recordID(record fortiRecord) string {
	if id := scalarRecordID(record.MKey); id != "" {
		return id
	}
	return scalarRecordID(record.ID)
}

// scalarRecordID accepts both shapes emitted by FortiOS for integer-mkey
// tables. Depending on firmware/endpoint, id and q_origin_key can be encoded as
// either JSON strings or JSON numbers; rejecting the numeric q_origin_key before
// considering id would make an otherwise valid collection response unusable.
func scalarRecordID(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		integer := int64(value)
		if value != float64(integer) {
			return ""
		}
		return strconv.FormatInt(integer, 10)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	case json.Number:
		integer, err := value.Int64()
		if err != nil {
			return ""
		}
		return strconv.FormatInt(integer, 10)
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
