package utils

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/nostosx/agentx/model"
)

// prepare adds the client's query parameters and bearer token to req.
//
// A token error keeps its chain, so model.IsRetryable decides from what
// the token source returned; an empty token is a permanent error.
func (c *HTTPClient) prepare(ctx context.Context, req *http.Request) error {
	if len(c.Query) > 0 {
		q := req.URL.Query()
		for k, vs := range c.Query {
			q.Del(k)
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		req.URL.RawQuery = q.Encode()
	}
	if c.Token == nil {
		return nil
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return fmt.Errorf("%s: get token: %w", c.Provider, err)
	}
	if tok == "" {
		return model.Permanent(fmt.Errorf("%s: get token: %w", c.Provider, errors.New("token source returned an empty token")))
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// HasAuth reports whether h carries one of the given credential headers or
// an Authorization header.
func HasAuth(h http.Header, names ...string) bool {
	if h.Get("Authorization") != "" {
		return true
	}
	for _, n := range names {
		if h.Get(n) != "" {
			return true
		}
	}
	return false
}

// VertexBaseURL returns the Vertex AI base URL of a publisher's models in
// a project and location, to which "/models/{model}:{method}" is appended
// (https://docs.cloud.google.com/vertex-ai/generative-ai/docs/learn/locations):
// the location "global" uses aiplatform.googleapis.com, the multi-regions
// "us" and "eu" use aiplatform.{location}.rep.googleapis.com, and other
// locations use {location}-aiplatform.googleapis.com.
func VertexBaseURL(project, location, publisher string) string {
	var host string
	switch location {
	case "global":
		host = "aiplatform.googleapis.com"
	case "us", "eu":
		host = "aiplatform." + location + ".rep.googleapis.com"
	default:
		host = location + "-aiplatform.googleapis.com"
	}
	return "https://" + host + "/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location) + "/publishers/" + publisher
}
