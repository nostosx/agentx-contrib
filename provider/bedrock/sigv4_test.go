package bedrock

import (
	"cmp"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The vectors are the AWS SigV4 test suite as published in
// https://github.com/awslabs/aws-c-auth/tree/main/tests/aws-signing-test-suite/v4
// (context.json, request.txt, header-canonical-request.txt and
// header-signature.txt of each case).
var (
	suiteCreds = Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	suiteTime  = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
)

const (
	emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	formHash  = "9095672bbd1f56dfc5b65f3e153adc8731a4a654192329106275f4c7b24d0b6e"
	stsToken  = "AQoDYXdzEPT//////////wEXAMPLEtc764bNrC9SAPBSM22wDOk4x4HIZ8j4FZTwdQWLWsKWHGBuFqwAeMicRXmxfpSPfIeoIYRqTflfKD8YUuwthAx7mSEI/qkPpKPi/kMcGdQrmGdeehM4IC1NtBmUpp2wUE8phUZampKsburEDy0KPkyQDYwT7WZ0wq5VSXDvp75YU9HFvlRd8Tx6q6fE8YQcHNVXAkiY9q6d+xo0rKwT38xVqr7ZD0u0iPPkUL64lIZbqBAz+scqKmlzm8FDrypNC9Yjc8fPOLn9FX9KSYvKTr4rvx3iSIlTJabIQwj2ICCR/oLxBA=="
)

func TestSigV4Suite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, path, query string
		header                    map[string][]string
		payload                   string
		canonical                 string // empty where the suite has none
		signature                 string
	}{
		{name: "get-vanilla", method: "GET", path: "/",
			canonical: "GET\n/\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\n" + emptyHash,
			signature: "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{name: "get-header-key-duplicate", method: "GET", path: "/",
			header:    map[string][]string{"My-Header1": {"value2", "value2", "value1"}},
			canonical: "GET\n/\n\nhost:example.amazonaws.com\nmy-header1:value2,value2,value1\nx-amz-date:20150830T123600Z\n\nhost;my-header1;x-amz-date\n" + emptyHash,
			signature: "c9d5ea9f3f72853aea855b47ea873832890dbdd183b4468f858259531a5138ea"},
		{name: "get-header-value-order", method: "GET", path: "/",
			header:    map[string][]string{"My-Header1": {"value4", "value1", "value3", "value2"}},
			signature: "08c7e5a9acfcfeb3ab6b2185e75ce8b1deb5e634ec47601a50643f830c755c01"},
		{name: "get-header-value-trim", method: "GET", path: "/",
			header:    map[string][]string{"My-Header1": {" value1"}, "My-Header2": {` "a   b   c"`}},
			canonical: "GET\n/\n\nhost:example.amazonaws.com\nmy-header1:value1\nmy-header2:\"a b c\"\nx-amz-date:20150830T123600Z\n\nhost;my-header1;my-header2;x-amz-date\n" + emptyHash,
			signature: "acc3ed3afb60bb290fc8d2dd0098b9911fcaa05412b367055dee359757a9c736"},
		{name: "get-slashes-unnormalized", method: "GET", path: "//example//",
			signature: "87cca117541a147f6df867677d98a7d80dff226d2bfca9e4ffa899665623c7e5"},
		{name: "get-space-unnormalized", method: "GET", path: "/example space/",
			canonical: "GET\n/example%20space/\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\n" + emptyHash,
			signature: "652487583200325589f1fba4c7e578f72c47cb61beeca81406b39ddec1366741"},
		{name: "get-unreserved", method: "GET", path: "/-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
			signature: "07ef7494c76fa4850883e2b006601f940f8a34d404d0cfa977f52a65bbf5f24f"},
		{name: "get-utf8", method: "GET", path: "/ሴ",
			canonical: "GET\n/%E1%88%B4\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\n" + emptyHash,
			signature: "8318018e0b0f223aa2bbf98705b62bb787dc9c0e678f255a891fd03141be5d85"},
		{name: "get-vanilla-empty-query-key", method: "GET", path: "/", query: "Param1=value1",
			signature: "a67d582fa61cc504c4bae71f336f98b97f1ea3c7a6bfe1b6e45aec72011b9aeb"},
		{name: "get-vanilla-query-order-encoded", method: "GET", path: "/", query: "Param-3=Value3&Param=Value2&%E1%88%B4=Value1",
			canonical: "GET\n/\n%E1%88%B4=Value1&Param=Value2&Param-3=Value3\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\n" + emptyHash,
			signature: "371d3713e185cc334048618a97f809c9ffe339c62934c032af5a0e595648fcac"},
		{name: "get-vanilla-query-order-key-case", method: "GET", path: "/", query: "Param2=value2&Param1=value1",
			signature: "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
		{name: "get-vanilla-query-unreserved", method: "GET", path: "/",
			query:     "-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz=-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
			signature: "9c3e54bfcdf0b19771a7f523ee5669cdf59bc7cc0884027167c21bb143a40197"},
		{name: "get-vanilla-utf8-query", method: "GET", path: "/", query: "ሴ=bar",
			signature: "2cdec8eed098649ff3a119c94853b13c643bcf08f8b0a1d91e12c9027818dd04"},
		{name: "get-vanilla-with-session-token", method: "GET", path: "/",
			header:    map[string][]string{"X-Amz-Security-Token": {"6e86291e8372ff2a2260956d9b8aae1d763fbf315fa00fa31553b73ebf194267"}},
			signature: "07ec1639c89043aa0e3e2de82b96708f198cceab042d4a97044c66dd9f74e7f8"},
		{name: "post-vanilla", method: "POST", path: "/",
			signature: "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{name: "post-header-key-sort", method: "POST", path: "/",
			header:    map[string][]string{"My-Header1": {"value1"}},
			signature: "c5410059b04c1ee005303aed430f6e6645f61f4dc9e1461ec8f8916fdf18852c"},
		{name: "post-header-value-case", method: "POST", path: "/",
			header:    map[string][]string{"My-Header1": {"VALUE1"}},
			signature: "cdbc9802e29d2942e5e10b5bccfdd67c5f22c7c4e8ae67b53629efa58b974b7d"},
		{name: "post-sts-header-before", method: "POST", path: "/",
			header:    map[string][]string{"X-Amz-Security-Token": {stsToken}},
			signature: "85d96828115b5dc0cfc3bd16ad9e210dd772bbebba041836c64533a82be05ead"},
		{name: "post-vanilla-query", method: "POST", path: "/", query: "Param1=value1",
			signature: "28038455d6de14eafc1f9222cf5aa6f1a96197d7deb8263271d420d138af7f11"},
		{name: "post-x-www-form-urlencoded", method: "POST", path: "/", payload: formHash,
			header: map[string][]string{
				"Content-Type": {"application/x-www-form-urlencoded"}, "Content-Length": {"13"},
				"X-Amz-Content-Sha256": {formHash},
			},
			signature: "d3875051da38690788ef43de4db0d8f280229d82040bfac253562e56c3f20e0b"},
		{name: "post-x-www-form-urlencoded-parameters", method: "POST", path: "/", payload: formHash,
			header: map[string][]string{
				"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}, "Content-Length": {"13"},
				"X-Amz-Content-Sha256": {formHash},
			},
			signature: "328d1b9eaadca9f5818ef05e8392801e091653bafec24fcab71e7344e7f51422"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{"Host": {"example.amazonaws.com"}, "X-Amz-Date": {"20150830T123600Z"}}
			maps.Copy(h, tc.header)
			in := sigV4Input{
				creds: suiteCreds, region: "us-east-1", service: "service", time: suiteTime,
				method: tc.method, path: tc.path, query: tc.query, header: h,
				payload: cmp.Or(tc.payload, emptyHash),
			}
			if tc.canonical != "" {
				if got, _ := canonicalRequest(in); got != tc.canonical {
					t.Errorf("canonical request\ngot  %q\nwant %q", got, tc.canonical)
				}
			}
			auth := signature(in)
			if !strings.HasSuffix(auth, "Signature="+tc.signature) {
				t.Fatalf("Authorization = %s, want signature %s", auth, tc.signature)
			}
		})
	}
}

func TestSignRequest(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	signRequest(req, nil, suiteCreds, "us-east-1", "service", suiteTime.In(time.FixedZone("x", 3600)))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want || req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("Authorization = %s, X-Amz-Date = %s", got, req.Header.Get("X-Amz-Date"))
	}
	// Temporary credentials add and sign the session token.
	creds := suiteCreds
	creds.SessionToken = "6e86291e8372ff2a2260956d9b8aae1d763fbf315fa00fa31553b73ebf194267" // gitleaks:allow AWS public test vector.
	signRequest(req, nil, creds, "us-east-1", "service", suiteTime)
	if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, "SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=07ec1639c89043aa0e3e2de82b96708f198cceab042d4a97044c66dd9f74e7f8") || // gitleaks:allow AWS public test vector.
		req.Header.Get("X-Amz-Security-Token") != creds.SessionToken {
		t.Fatalf("Authorization = %s", got)
	}
	// Signing again with long-term credentials drops the stale token.
	signRequest(req, nil, suiteCreds, "us-east-1", "service", suiteTime)
	if req.Header.Get("X-Amz-Security-Token") != "" || !strings.HasSuffix(req.Header.Get("Authorization"), "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31") {
		t.Fatalf("headers = %v", req.Header)
	}
}

func TestCanonicalURIEncodesEscapedPathAgain(t *testing.T) {
	t.Parallel()
	// The AWS SDKs escape the request path once more for every service
	// except S3, so the ":" of a model ID, sent as %3A, is signed as %253A.
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/"+uriEncode("us.anthropic.claude-sonnet-4-5-20250929-v1:0", true)+"/converse", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.EscapedPath(); got != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse" {
		t.Fatalf("escaped path = %s", got)
	}
	if got := canonicalURI(req.URL.EscapedPath()); got != "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%253A0/converse" {
		t.Fatalf("canonical URI = %s", got)
	}
	if got := uriEncode("arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.x", true); got != "arn%3Aaws%3Abedrock%3Aus-east-1%3A123456789012%3Ainference-profile%2Fus.x" {
		t.Fatalf("uriEncode = %s", got)
	}
}
