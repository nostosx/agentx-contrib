package bedrock

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// AWS Signature Version 4, as described at
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html.

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	// signingName is the SigV4 service name of the bedrock-runtime
	// endpoint, which differs from its host name.
	signingName = "bedrock"
	amzDate     = "20060102T150405Z"
)

// Credentials are AWS credentials for signing requests.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is set for temporary credentials, such as those of an
	// assumed role, and sent as X-Amz-Security-Token.
	SessionToken string
}

// signRequest signs req, whose body is body, for service in region at t.
// It sets X-Amz-Date, X-Amz-Security-Token for temporary credentials, and
// Authorization. The signed headers are Host, Content-Type and the X-Amz-*
// headers; other headers, such as those added with WithHeader, are sent
// unsigned.
//
// As for every AWS service except Amazon S3, the canonical URI encodes the
// already escaped request path once more, as the AWS SDKs do; see
// DisableURIPathEscaping in
// https://github.com/aws/aws-sdk-go-v2/blob/main/aws/signer/v4/v4.go.
func signRequest(req *http.Request, body []byte, creds Credentials, region, service string, t time.Time) {
	t = t.UTC()
	req.Header.Set("X-Amz-Date", t.Format(amzDate))
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	} else {
		req.Header.Del("X-Amz-Security-Token")
	}
	signed := http.Header{}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			signed[k] = vs
		}
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed.Set("Host", host)
	path := req.URL.EscapedPath()
	if req.URL.Opaque != "" {
		path = req.URL.Opaque
	}
	auth := signature(sigV4Input{
		creds:   creds,
		region:  region,
		service: service,
		time:    t,
		method:  req.Method,
		path:    path,
		query:   req.URL.RawQuery,
		header:  signed,
		payload: hashHex(body),
	})
	req.Header.Set("Authorization", auth)
}

// sigV4Input holds the parts of a request that are signed.
type sigV4Input struct {
	creds           Credentials
	region, service string
	time            time.Time
	method          string
	// path is URI-encoded once more to form the canonical URI.
	path  string
	query string
	// header holds the headers to sign, including Host.
	header http.Header
	// payload is the hex-encoded SHA-256 hash of the body.
	payload string
}

// canonicalRequest returns the canonical request of in and its signed
// header list.
func canonicalRequest(in sigV4Input) (string, string) {
	canonicalHeaders, signedHeaders := canonicalHeaders(in.header)
	return strings.Join([]string{
		in.method,
		canonicalURI(in.path),
		canonicalQuery(in.query),
		canonicalHeaders,
		signedHeaders,
		in.payload,
	}, "\n"), signedHeaders
}

// signature returns the Authorization header value for in.
func signature(in sigV4Input) string {
	canonical, signedHeaders := canonicalRequest(in)
	date := in.time.UTC().Format("20060102")
	scope := date + "/" + in.region + "/" + in.service + "/aws4_request"
	toSign := sigV4Algorithm + "\n" + in.time.UTC().Format(amzDate) + "\n" + scope + "\n" + hashHex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+in.creds.SecretAccessKey), date)
	key = hmacSHA256(key, in.region)
	key = hmacSHA256(key, in.service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	return sigV4Algorithm + " Credential=" + in.creds.AccessKeyID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + sig
}

// canonicalURI URI-encodes every byte of path except the unreserved
// characters and '/'. An empty path is "/".
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return uriEncode(path, false)
}

// canonicalQuery encodes each name and value of a raw query and sorts the
// pairs by name, then value.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type pair struct{ k, v string }
	var pairs []pair
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		pairs = append(pairs, pair{uriEncode(k, true), uriEncode(v, true)})
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := strings.Compare(a.k, b.k); c != 0 {
			return c
		}
		return strings.Compare(a.v, b.v)
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k + "=" + p.v)
	}
	return b.String()
}

// canonicalHeaders returns the canonical header block, each header ending
// in a newline, and the signed header list. Names are lowercased and
// sorted, values trimmed with inner runs of spaces collapsed, and the values
// of a repeated header joined with commas in their original order.
func canonicalHeaders(h http.Header) (string, string) {
	values := map[string][]string{}
	for k, vs := range h {
		lk := strings.ToLower(k)
		for _, v := range vs {
			values[lk] = append(values[lk], strings.Join(strings.Fields(v), " "))
		}
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k + ":" + strings.Join(values[k], ",") + "\n")
	}
	return b.String(), strings.Join(names, ";")
}

// uriEncode percent-encodes every byte of s except the unreserved
// characters A-Z, a-z, 0-9, '-', '.', '_' and '~', with upper-case hex
// digits. '/' is kept unless encodeSlash is set.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
