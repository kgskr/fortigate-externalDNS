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
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
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

// errZoneApexUnsupported is returned for a record at the zone apex. FortiGate
// dns-entry hostnames are relative to the dns-database domain, and the apex
// spelling has not been verified against a device, so the write fails closed.
var errZoneApexUnsupported = errors.New("FortiGate zone apex records are not supported")

// errInvalidProviderID is returned for a provider ID that is not a plain
// non-negative integer. The ID becomes a URL path segment, so anything else
// (for example "..") could redirect a write to a different CMDB object.
var errInvalidProviderID = errors.New("invalid FortiGate provider ID")

// errUnsupportedRecordType is returned for a record type FortiGate writes are
// not implemented for; the value would otherwise land in the wrong field.
var errUnsupportedRecordType = errors.New("unsupported FortiGate record type")

// errUnknownOperation is returned for an operation type applyOne cannot execute,
// so it is never counted as succeeded.
var errUnknownOperation = errors.New("unknown operation type")

var providerIDPattern = regexp.MustCompile(`^[0-9]+$`)

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

	// Timing and limits are fields so tests can make retries instant and
	// deterministic.
	sleep            func(ctx context.Context, d time.Duration) error
	jitter           func() float64 // uniform in [0,1)
	now              func() time.Time
	maxResponseBytes int64
}

const (
	fortiListPageSize = 1000

	// defaultMaxResponseBytes bounds a single response body read. A full
	// 1000-row page is well under 1 MiB, so this only trips on a misbehaving
	// endpoint.
	defaultMaxResponseBytes = 32 << 20

	retryBaseDelay     = 100 * time.Millisecond
	retryMaxDelay      = 5 * time.Second
	retryAfterMaxDelay = 30 * time.Second
)

// fortiResponse is the FortiOS collection envelope. size is the total row count
// of the table, matched_count the rows in this page. limit_reached and next_idx
// are deliberately not decoded: next_idx is inconsistent between pages.
type fortiResponse struct {
	Results      []fortiRecord `json:"results"`
	Size         *int          `json:"size"`
	MatchedCount *int          `json:"matched_count"`
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

		sleep:            sleepContext,
		jitter:           rand.Float64,
		now:              time.Now,
		maxResponseBytes: defaultMaxResponseBytes,
	}, nil
}

// Close releases idle connections so a rotated-out client does not pin sockets.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) ListRecords(ctx context.Context) ([]dns.Endpoint, error) {
	var records []fortiRecord
	seenProviderIDs := map[string]struct{}{}
	start := 0
	total := -1
	revision := ""
	pages := 0

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
		if response.Size == nil {
			return nil, errors.New("FortiGate list response is missing pagination metadata")
		}
		if *response.Size < 0 {
			return nil, fmt.Errorf("FortiGate list response has negative size %d", *response.Size)
		}
		if total == -1 {
			total = *response.Size
		} else if *response.Size != total {
			return nil, fmt.Errorf("FortiGate list size changed during pagination: %d to %d", total, *response.Size)
		}
		if response.MatchedCount != nil && *response.MatchedCount != len(response.Results) {
			return nil, fmt.Errorf("FortiGate list response matched_count %d does not match %d returned records", *response.MatchedCount, len(response.Results))
		}
		responseRevision := ""
		if response.Revision != nil {
			responseRevision = strings.TrimSpace(*response.Revision)
		}
		if pages == 0 {
			// A single complete page may omit the revision (the content digest
			// covers that case); it is enforced once a second page is needed.
			revision = responseRevision
		} else {
			if responseRevision == "" || revision == "" {
				return nil, errors.New("FortiGate paginated list response is missing a non-empty revision")
			}
			if responseRevision != revision {
				return nil, fmt.Errorf("FortiGate list revision changed during pagination: %q to %q", revision, responseRevision)
			}
		}
		pages++

		for _, record := range response.Results {
			providerID := strings.TrimSpace(recordID(record))
			if providerID == "" {
				return nil, errors.New("FortiGate list response contains a record without a provider ID")
			}
			if !providerIDPattern.MatchString(providerID) {
				return nil, fmt.Errorf("FortiGate list response contains a record with a non-numeric provider ID %q: %w", providerID, errInvalidProviderID)
			}
			if _, exists := seenProviderIDs[providerID]; exists {
				return nil, fmt.Errorf("FortiGate list response repeats provider ID %q", providerID)
			}
			seenProviderIDs[providerID] = struct{}{}
			records = append(records, record)
		}

		if len(records) > total {
			return nil, fmt.Errorf("FortiGate list response is inconsistent: collected %d records but size is %d", len(records), total)
		}
		if len(records) == total {
			break
		}
		if len(response.Results) == 0 {
			return nil, fmt.Errorf("FortiGate list pagination did not advance: start=%d returned no records with %d of %d collected", start, len(records), total)
		}
		start += len(response.Results)
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
	// Marshal each record once and sort on the bytes; marshaling inside the
	// comparator would cost O(n log n) encodes.
	encoded := make([][]byte, 0, len(records))
	for _, endpoint := range records {
		endpoint = endpoint.Normalize()
		raw, err := json.Marshal(record{
			ProviderID: endpoint.ProviderID, Zone: endpoint.Zone, DNSName: endpoint.DNSName,
			RecordType: endpoint.RecordType, Targets: append([]string(nil), endpoint.Targets...),
			TTL: endpoint.TTL, Disabled: endpoint.Disabled,
		})
		if err != nil {
			return "", fmt.Errorf("serialize FortiGate snapshot revision: %w", err)
		}
		encoded = append(encoded, raw)
	}
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i], encoded[j]) < 0 })
	// Byte-identical to json.Marshal of the sorted record slice.
	data := append(append([]byte{'['}, bytes.Join(encoded, []byte{','})...), ']')
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (c *Client) Apply(ctx context.Context, operations []plan.Operation, dryRun bool) error {
	_, err := c.ApplyWithResults(ctx, operations, dryRun)
	return err
}

// ApplyWithResults preserves the result of each operation, including work
// blocked by a failed prerequisite or cancellation. Reasons are fixed codes:
// provider responses and credentials must never enter persisted plan status.
func (c *Client) ApplyWithResults(ctx context.Context, operations []plan.Operation, dryRun bool) ([]plan.OperationOutcome, error) {
	var errs []error
	outcomes := make([]plan.OperationOutcome, 0, len(operations))
	recordOutcome := func(operation plan.Operation, result plan.ApplyOutcome, reason string) {
		outcomes = append(outcomes, plan.OperationOutcome{
			OperationID: plan.SanitizeOperation(operation).ID, Result: result, Reason: reason,
		})
	}
	var attempted, succeeded, failed, skipped, conflict int
	failedPrerequisiteGroups := map[string]struct{}{}

	for index, operation := range operations {
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
			for _, remaining := range operations[index:] {
				recordOutcome(remaining, plan.ApplyBlocked, "context-canceled")
			}
			break
		}
		if operation.Type == plan.OperationConflict {
			conflict++
			c.recordOperation(operation.Type, "conflict")
			c.loggerOrDefault().Warn("planning conflict; skipping operation", "operation", operation.String())
			recordOutcome(operation, plan.ApplyBlocked, "planning-conflict")
			continue
		}
		if dryRun {
			skipped++
			c.recordOperation(operation.Type, "skipped")
			c.loggerOrDefault().Info("dry-run planned operation", "operation", operation.String())
			recordOutcome(operation, plan.ApplyBlocked, "dry-run")
			continue
		}
		if isCleanupOperation(operation.Type) {
			if _, blocked := failedPrerequisiteGroups[operation.Current.MutationGroupKey()]; blocked {
				skipped++
				c.recordOperation(operation.Type, "skipped")
				c.loggerOrDefault().Warn("dependent cleanup skipped after failed prerequisite mutation", "operation", operation.String())
				recordOutcome(operation, plan.ApplyBlocked, "prerequisite-failed")
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
			recordOutcome(operation, plan.ApplyFailed, "provider-request-failed")
			continue
		}
		succeeded++
		c.recordOperation(operation.Type, "applied")
		c.loggerOrDefault().Info("apply operation succeeded", "operation", operation.String())
		recordOutcome(operation, plan.ApplySucceeded, "")
	}

	c.loggerOrDefault().Info("apply summary",
		"attempted", attempted, "succeeded", succeeded, "failed", failed,
		"skipped", skipped, "conflict", conflict, "dryRun", dryRun)
	return outcomes, errors.Join(errs...)
}

func (c *Client) applyOne(ctx context.Context, operation plan.Operation) error {
	switch operation.Type {
	case plan.OperationCreate:
		body, err := endpointToRecord(operation.Desired, c.cfg.Zone)
		if err != nil {
			return err
		}
		req, err := c.newRequest(ctx, http.MethodPost, c.recordsPath(""), body)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	case plan.OperationUpdate, plan.OperationDeactivate, plan.OperationReplace:
		id, err := currentProviderID(operation)
		if err != nil {
			return fmt.Errorf("cannot %s %q: %w", operation.Type, operation.Current.DNSName, err)
		}
		body, err := endpointToRecord(operation.Desired, c.cfg.Zone)
		if err != nil {
			return err
		}
		req, err := c.newRequest(ctx, http.MethodPut, c.recordsPath(id), body)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	case plan.OperationDelete:
		id, err := currentProviderID(operation)
		if err != nil {
			return fmt.Errorf("cannot delete %q: %w", operation.Current.DNSName, err)
		}
		req, err := c.newRequest(ctx, http.MethodDelete, c.recordsPath(id), nil)
		if err != nil {
			return err
		}
		return c.doJSON(req, nil)
	default:
		return fmt.Errorf("%w %q", errUnknownOperation, operation.Type)
	}
}

// currentProviderID returns the validated numeric dns-entry id of the
// operation's current record. It is checked again here, not only at list time,
// because the value is interpolated into a URL path.
func currentProviderID(operation plan.Operation) (string, error) {
	id := strings.TrimSpace(operation.Current.ProviderID)
	if id == "" {
		return "", errMissingProviderID
	}
	if !providerIDPattern.MatchString(id) {
		return "", errInvalidProviderID
	}
	return id, nil
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
	// requestPath is already escaped. Set Path and RawPath together so the zone
	// is escaped exactly once on the wire.
	rawPath := strings.TrimRight(endpoint.EscapedPath(), "/") + requestPath
	unescaped, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, err
	}
	endpoint.Path = unescaped
	endpoint.RawPath = rawPath
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
		var retryAfter time.Duration
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
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
			closeErr := resp.Body.Close()
			switch {
			case readErr == nil && int64(len(raw)) > c.maxResponseBytes:
				// An oversized body is not transient; fail without retrying.
				return fmt.Errorf("fortigate API %s %s response body exceeds %d bytes", req.Method, req.URL.Path, c.maxResponseBytes)
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
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
					retryAfter = c.parseRetryAfter(resp.Header.Get("Retry-After"))
				}
				if !retryable || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500) {
					return lastErr
				}
			}
		}
		if attempt < attempts {
			if err := c.sleep(ctx, c.retryDelay(attempt, retryAfter)); err != nil {
				return err
			}
		}
	}
	return lastErr
}

// retryDelay is exponential backoff with full jitter (uniform in [0, min(cap,
// base*2^(attempt-1)))). A server-provided Retry-After is a floor on the wait.
func (c *Client) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	ceiling := retryBaseDelay
	for i := 1; i < attempt && ceiling < retryMaxDelay; i++ {
		ceiling *= 2
	}
	if ceiling > retryMaxDelay {
		ceiling = retryMaxDelay
	}
	delay := time.Duration(c.jitter() * float64(ceiling))
	if retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

// parseRetryAfter reads a Retry-After header (delta-seconds or HTTP-date),
// capped at retryAfterMaxDelay. Invalid or past values yield 0. The wait is
// still bounded by the request context in the caller.
func (c *Client) parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var delay time.Duration
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(retryAfterMaxDelay/time.Second) {
			return retryAfterMaxDelay
		}
		delay = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(value); err == nil {
		delay = when.Sub(c.now())
	}
	if delay <= 0 {
		return 0
	}
	if delay > retryAfterMaxDelay {
		return retryAfterMaxDelay
	}
	return delay
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
		targets = appendIfNotEmpty(targets, qualifiedCanonicalName(r.CanonicalName, zone))
	default:
		targets = appendIfNotEmpty(targets, r.IP)
		targets = appendIfNotEmpty(targets, r.IPv6)
		targets = appendIfNotEmpty(targets, r.CanonicalName)
	}
	return dns.Endpoint{
		DNSName:    qualifiedHostname(r.Hostname, zone),
		RecordType: recordType,
		Targets:    targets,
		TTL:        r.TTL,
		Zone:       zone,
		ProviderID: recordID(r),
		Disabled:   strings.EqualFold(r.Status, "disable") || strings.EqualFold(r.Status, "disabled"),
	}
}

// qualifiedHostname turns a zone-relative FortiGate dns-entry hostname into the
// fully qualified name used everywhere else in the controller. The device
// appends the dns-database domain to every hostname, so a stored value that
// already looks like an FQDN is published under the zone twice and is mapped
// accordingly rather than being mistaken for the intended name.
func qualifiedHostname(hostname, zone string) string {
	hostname = dns.NormalizeDNSName(hostname)
	if hostname == "" {
		return ""
	}
	return hostname + "." + dns.NormalizeDNSName(zone)
}

// qualifiedCanonicalName maps a stored CNAME target to its served name. Like
// hostname, canonical-name is zone-relative unless it ends with a dot, so
// "lb.example.net" is served as "lb.example.net.<zone>".
func qualifiedCanonicalName(name, zone string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasSuffix(name, ".") {
		return dns.NormalizeDNSName(name)
	}
	return qualifiedHostname(name, zone)
}

// relativeHostname is the inverse of qualifiedHostname for writes.
func relativeHostname(name, zone string) (string, error) {
	name = dns.NormalizeDNSName(name)
	zone = dns.NormalizeDNSName(zone)
	if name == zone {
		return "", fmt.Errorf("%q: %w", name, errZoneApexUnsupported)
	}
	relative, ok := strings.CutSuffix(name, "."+zone)
	if !ok || relative == "" {
		return "", fmt.Errorf("hostname %q is outside FortiGate zone %q", name, zone)
	}
	return relative, nil
}

func endpointToRecord(endpoint dns.Endpoint, zone string) (fortiRecord, error) {
	endpoint = endpoint.Normalize()
	switch endpoint.RecordType {
	case dns.RecordA, dns.RecordAAAA, dns.RecordCNAME:
	default:
		return fortiRecord{}, fmt.Errorf("%w %q", errUnsupportedRecordType, endpoint.RecordType)
	}
	hostname, err := relativeHostname(endpoint.DNSName, zone)
	if err != nil {
		return fortiRecord{}, err
	}
	record := fortiRecord{
		Hostname: hostname,
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
			// The trailing dot makes the target absolute; without it FortiGate
			// appends the zone.
			record.CanonicalName = dns.NormalizeDNSName(endpoint.Targets[0]) + "."
		default:
			record.IP = endpoint.Targets[0]
		}
	}
	return record, nil
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
