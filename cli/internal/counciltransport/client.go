// Package counciltransport implements the native council API transport over
// the openai_compatible contracts extracted from the legacy PowerShell
// transport (Council.Transport.ps1). The legacy OpenCode/PowerShell review is
// not a dependency of this path.
package counciltransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds mirrored from the legacy contract: request_timeout_seconds defaults
// to 300 and is clamped to 60..900 (Council.Common.ps1:268-270); input and
// output limits default to 1 MiB and are clamped to 1..16777216 bytes
// (Council.Transport.ps1:331-332,344-346).
const (
	DefaultTimeout      = 300 * time.Second
	MinTimeout          = 60 * time.Second
	MaxTimeout          = 900 * time.Second
	DefaultMaxBodyBytes = int64(1048576)
	MaxBodyBytesLimit   = int64(16777216)
	maxExcerptBytes     = int64(512)
	chatCompletionsPath = "/chat/completions"
)

var (
	// ErrBeforeDispatch marks failures provable before the request reached the
	// provider (never-connected dial failures and pre-dispatch validation).
	ErrBeforeDispatch = errors.New("counciltransport: request failed before dispatch")
	// ErrUnknownAfterDispatch marks timeout or transport failures after the
	// request was sent: the effect is unknown and must not be auto-retried.
	ErrUnknownAfterDispatch = errors.New("counciltransport: request failed after dispatch; effect is unknown and must not be auto-retried")
	// ErrRedirectRefused marks any provider redirect; redirects are never followed.
	ErrRedirectRefused = errors.New("counciltransport: council redirect refused")
	// ErrResponseBound marks a response body above the output byte limit.
	ErrResponseBound = errors.New("counciltransport: council response exceeds the output limit")
	// ErrInvalidResponse marks a response body that is not one strict provider envelope.
	ErrInvalidResponse = errors.New("counciltransport: council response is invalid")
)

// RedirectError is returned for any 3xx status; the location is reported but
// never fetched.
type RedirectError struct {
	Status   int
	Location string
}

func (e *RedirectError) Error() string {
	location := e.Location
	if location == "" {
		location = "(none)"
	}
	return fmt.Sprintf("counciltransport: council redirect refused: status %d, location %s", e.Status, location)
}

func (e *RedirectError) Unwrap() error { return ErrRedirectRefused }

// ProviderStatusError is returned for any non-200 status that is not a
// redirect. The legacy taxonomy (4xx failed-before-acceptance, 5xx
// unknown-after-dispatch) is recoverable from Code.
type ProviderStatusError struct {
	Code    int
	Excerpt string
}

func (e *ProviderStatusError) Error() string {
	return fmt.Sprintf("counciltransport: council provider returned status %d: %s", e.Code, e.Excerpt)
}

// Client posts one chat completion per call to BaseURL/chat/completions.
// Timeout defaults to 300s (0) and must lie within 60s..900s. MaxRequestBytes
// and MaxResponseBytes default to 1 MiB (0) and must lie within 1..16777216.
type Client struct {
	HTTP             *http.Client
	BaseURL          string
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

func (c Client) Chat(ctx context.Context, req Request, creds Credentials) (Response, error) {
	timeout, maxRequest, maxResponse, err := c.limits()
	if err != nil {
		return Response{}, beforeDispatch(err)
	}
	if strings.TrimSpace(creds.APIKey) == "" {
		return Response{}, beforeDispatch(errors.New("counciltransport: council credential is empty"))
	}
	if err := ctx.Err(); err != nil {
		return Response{}, beforeDispatch(fmt.Errorf("counciltransport: council request was cancelled before dispatch: %v", err))
	}
	body, err := req.Canonical()
	if err != nil {
		return Response{}, beforeDispatch(err)
	}
	if int64(len(body)) > maxRequest {
		return Response{}, beforeDispatch(errors.New("counciltransport: council request exceeds the input limit"))
	}
	endpoint, err := chatEndpointURL(c.BaseURL)
	if err != nil {
		return Response{}, beforeDispatch(err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, beforeDispatch(err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+creds.APIKey)

	resp, err := c.httpClient().Do(httpRequest)
	if err != nil {
		message := redact(innerErrorMessage(err), creds.APIKey)
		if isDialError(err) {
			return Response{}, beforeDispatch(errors.New(message))
		}
		return Response{}, fmt.Errorf("%w: %s", ErrUnknownAfterDispatch, message)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return Response{}, &RedirectError{Status: resp.StatusCode, Location: redact(resp.Header.Get("Location"), creds.APIKey)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &ProviderStatusError{Code: resp.StatusCode, Excerpt: responseExcerpt(requestCtx, resp, creds)}
	}

	payload, err := readBoundedBody(requestCtx, resp, maxResponse)
	if err != nil {
		if errors.Is(err, ErrResponseBound) {
			return Response{}, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Response{}, fmt.Errorf("%w: council response body was not delivered before the deadline", ErrUnknownAfterDispatch)
		}
		return Response{}, fmt.Errorf("%w: %s", ErrUnknownAfterDispatch, redact(err.Error(), creds.APIKey))
	}
	if !utf8.Valid(payload) {
		return Response{}, fmt.Errorf("%w: council response is not valid UTF-8", ErrInvalidResponse)
	}
	parsed, err := ParseResponse(payload)
	if err != nil {
		return Response{}, redactError(err, creds.APIKey)
	}
	return parsed, nil
}

func (c Client) limits() (time.Duration, int64, int64, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < MinTimeout || timeout > MaxTimeout {
		return 0, 0, 0, fmt.Errorf("counciltransport: council timeout must be between %d and %d seconds", int(MinTimeout.Seconds()), int(MaxTimeout.Seconds()))
	}
	maxRequest := c.MaxRequestBytes
	if maxRequest == 0 {
		maxRequest = DefaultMaxBodyBytes
	}
	if maxRequest < 1 || maxRequest > MaxBodyBytesLimit {
		return 0, 0, 0, errors.New("counciltransport: council input limit must be between 1 and 16777216 bytes")
	}
	maxResponse := c.MaxResponseBytes
	if maxResponse == 0 {
		maxResponse = DefaultMaxBodyBytes
	}
	if maxResponse < 1 || maxResponse > MaxBodyBytesLimit {
		return 0, 0, 0, errors.New("counciltransport: council output limit must be between 1 and 16777216 bytes")
	}
	return timeout, maxRequest, maxResponse, nil
}

// httpClient mirrors AllowAutoRedirect=false and UseProxy=false of the legacy
// sender: redirects are never followed, even for an injected client.
func (c Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: refuseRedirect}
	}
	outbound := *c.HTTP
	outbound.CheckRedirect = refuseRedirect
	return &outbound
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func chatEndpointURL(baseURL string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", errors.New("counciltransport: council base URL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("counciltransport: invalid council endpoint URL: %v", err)
	}
	if !parsed.IsAbs() || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("counciltransport: invalid council endpoint URL: scheme must be http or https")
	}
	host := parsed.Hostname()
	if strings.TrimSpace(host) == "" {
		return "", errors.New("counciltransport: invalid council endpoint URL: host is empty")
	}
	if parsed.User != nil {
		return "", errors.New("counciltransport: invalid council endpoint URL: userinfo is forbidden")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("counciltransport: invalid council endpoint URL: query and fragment are forbidden")
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || dotSegmentPattern.MatchString(path) {
		return "", errors.New("counciltransport: invalid council endpoint base path")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(host) {
		return "", errors.New("counciltransport: invalid council endpoint URL: plain HTTP is allowed only for loopback council endpoints")
	}
	return parsed.Scheme + "://" + parsed.Host + strings.TrimRight(path, "/") + chatCompletionsPath, nil
}

var dotSegmentPattern = regexp.MustCompile(`(^|/)\.\.?(/|$)`)

func isLoopbackHost(host string) bool {
	name := strings.ToLower(strings.Trim(host, "[]"))
	if name == "localhost" || name == "127.0.0.1" || name == "::1" {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// readBoundedBody streams the body under the operation deadline with a hard
// byte cap, mirroring Read-BSLFlowCouncilHttpResponseBody: a declared
// Content-Length above the cap and a streamed length above the cap are both
// terminal bound errors.
func readBoundedBody(ctx context.Context, resp *http.Response, maxBytes int64) ([]byte, error) {
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%w: council response exceeds the output limit", ErrResponseBound)
	}
	type readOutcome struct {
		data []byte
		err  error
	}
	done := make(chan readOutcome, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		done <- readOutcome{data: data, err: err}
	}()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			return nil, outcome.err
		}
		if int64(len(outcome.data)) > maxBytes {
			return nil, fmt.Errorf("%w: council response exceeds the output limit", ErrResponseBound)
		}
		return outcome.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func responseExcerpt(ctx context.Context, resp *http.Response, creds Credentials) string {
	data, err := readBoundedBody(ctx, resp, maxExcerptBytes)
	if err != nil && !errors.Is(err, ErrResponseBound) {
		return redact(fmt.Sprintf("<response body unavailable: %v>", err), creds.APIKey)
	}
	text := string(data)
	if errors.Is(err, ErrResponseBound) || int64(len(data)) >= maxExcerptBytes {
		text += "..."
	}
	return redact(text, creds.APIKey)
}

// isDialError identifies failures provable before dispatch: the connection to
// the provider was never established, so no request could have been delivered.
func isDialError(err error) bool {
	var opError *net.OpError
	if errors.As(err, &opError) && opError.Op == "dial" {
		return true
	}
	var dnsError *net.DNSError
	return errors.As(err, &dnsError)
}

// innerErrorMessage drops the request URL from transport errors, mirroring the
// legacy diagnostic projection that never carries a URL.
func innerErrorMessage(err error) string {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err.Error()
	}
	return err.Error()
}

func beforeDispatch(err error) error {
	return fmt.Errorf("%w: %s", ErrBeforeDispatch, err)
}

func redactError(err error, secrets ...string) error {
	return errors.New(redact(err.Error(), secrets...))
}

// redact mirrors Protect-BSLFlowCouncilDiagnostic: explicit secrets, bearer
// tokens, token JSON fields and Authorization headers never survive into
// diagnostics.
func redact(text string, secrets ...string) string {
	clean := text
	for _, secret := range secrets {
		if secret != "" {
			clean = strings.ReplaceAll(clean, secret, "<redacted>")
		}
	}
	clean = bearerPattern.ReplaceAllString(clean, "Bearer <redacted>")
	clean = tokenJSONPattern.ReplaceAllString(clean, `"token": "<redacted>"`)
	clean = authorizationPattern.ReplaceAllString(clean, "Authorization: <redacted>")
	return clean
}

var (
	bearerPattern        = regexp.MustCompile(`(?is)\bBearer\s+[^\s,;]+`)
	tokenJSONPattern     = regexp.MustCompile(`(?i)"token"\s*:\s*"[^"]*"`)
	authorizationPattern = regexp.MustCompile(`(?is)Authorization\s*:\s*[^\r\n]+`)
)
