package jigsawstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	netURL "net/url"
)

// Querier is an interface for objects that can modify URL query parameters.
type Querier interface {
	URLQuery(u *netURL.URL)
}

// requestOption configures an HTTP request before it is sent.
type requestOption func(*http.Request) error

// newRequest creates a new HTTP request with the given options.
func newRequest(
	ctx context.Context,
	setHeaders func(*http.Request),
	method string,
	url string,
	opts ...requestOption,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if setHeaders != nil {
		setHeaders(req)
	}
	for _, opt := range opts {
		if err := opt(req); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// withBody marshals v to JSON and sets it as the request body.
func withBody(v any) requestOption {
	return func(req *http.Request) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("failed to marshal body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		return nil
	}
}

// withContentType sets the Content-Type header on the request.
func withContentType(contentType string) requestOption {
	return func(req *http.Request) error {
		req.Header.Set("Content-Type", contentType)
		return nil
	}
}

// withQuerier sets the URL query parameters using the provided Querier.
func withQuerier(q Querier) requestOption {
	return func(req *http.Request) error {
		if q != nil {
			q.URLQuery(req.URL)
		}
		return nil
	}
}
