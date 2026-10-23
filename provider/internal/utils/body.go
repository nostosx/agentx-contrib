package utils

import "io"

// URLErrorBody wraps a response body so that its read errors, other than
// io.EOF, are reported as *url.Error for rawURL, as HTTPClient.Post does
// for the bodies it returns. Providers that send their own requests use it
// so that model.IsRetryable treats a connection lost mid-response as a
// transport failure.
func URLErrorBody(body io.ReadCloser, rawURL string) io.ReadCloser {
	return &bodyReader{ReadCloser: body, url: rawURL}
}
