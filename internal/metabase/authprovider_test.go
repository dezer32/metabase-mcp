package metabase

import (
	"net/http"
	"strings"
	"testing"
)

func hdr(vals ...string) http.Header {
	h := http.Header{}
	for _, v := range vals {
		h.Add("WWW-Authenticate", v)
	}
	return h
}

func TestClassify401(t *testing.T) {
	tests := []struct {
		name          string
		header        http.Header
		wantRetryable bool
		diagContains  string
	}{
		{
			name:          "no header means expired token, retryable",
			header:        http.Header{},
			wantRetryable: true,
		},
		{
			name:          "invalid_token is retryable",
			header:        hdr(`Bearer error="invalid_token", error_description="expired"`),
			wantRetryable: true,
		},
		{
			name:          "insufficient_scope is not retryable",
			header:        hdr(`Bearer error="insufficient_scope", error_description="need mb:full"`),
			wantRetryable: false,
			diagContains:  "insufficient_scope",
		},
		{
			name:          "other bearer error is not retryable",
			header:        hdr(`Bearer error="invalid_request", error_description="bad audience"`),
			wantRetryable: false,
			diagContains:  "invalid_request",
		},
		{
			name:          "bare bearer challenge without error is retryable",
			header:        hdr(`Bearer realm="metabase"`),
			wantRetryable: true,
		},
		{
			name:          "non-bearer challenge is treated as retryable",
			header:        hdr(`Basic realm="metabase"`),
			wantRetryable: true,
		},
		{
			name:          "malformed header is retryable",
			header:        hdr(`Bearer error=`),
			wantRetryable: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			retryable, diag := classify401(tc.header)
			if retryable != tc.wantRetryable {
				t.Errorf("retryable: got %v, want %v (diag=%q)", retryable, tc.wantRetryable, diag)
			}
			if tc.diagContains != "" && !strings.Contains(diag, tc.diagContains) {
				t.Errorf("diag %q should contain %q", diag, tc.diagContains)
			}
		})
	}
}
