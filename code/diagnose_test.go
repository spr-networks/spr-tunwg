package main

import (
	"strings"
	"testing"
)

func TestDiagnose(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want string
	}{
		{
			name: "relay DNS timeout",
			log:  `failed to connect err="Post https://l.tunwg.com/add: dial tcp: lookup l.tunwg.com: i/o timeout"`,
			want: "Can't reach the tunwg relay",
		},
		{
			name: "relay rejects token",
			log:  `failed to connect err="error adding peer: 403 Forbidden"`,
			want: "Relay rejected the key or auth token",
		},
		{
			name: "HTTPS relay rejects upgrade",
			log:  `failed to connect err="unexpected relay status: 401 Unauthorized"`,
			want: "Relay rejected the key or auth token",
		},
		{
			name: "TLS certificate",
			log:  `failed to connect err="tls: failed to verify certificate: x509: certificate has expired"`,
			want: "TLS problem talking to the relay",
		},
		{
			name: "state volume",
			log:  `failed to connect err="open /state/plugins/spr-tunwg/tunwg/keys/home: read-only file system"`,
			want: "Can't write the tunnel state directory",
		},
		{
			name: "binary missing",
			log:  `fork/exec /usr/local/bin/tunwg: no such file or directory`,
			want: "Couldn't start the tunwg binary",
		},
		{
			name: "invalid basic auth",
			log:  `invalid value for --limit. Use htpasswd format`,
			want: "Basic auth configuration is invalid",
		},
		{
			name: "unknown",
			log:  `failed to connect err="wireguard setup failed"`,
			want: "before publishing a URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, hint := diagnose(tt.log, 1)
			if !strings.Contains(reason, tt.want) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tt.want)
			}
			if hint == "" {
				t.Fatal("expected an actionable hint")
			}
		})
	}
}

func TestStartupErrorIncludesDiagnosisAndTail(t *testing.T) {
	err := startupError(ForwardStatus{
		LastErrorReason: "Can't reach the tunwg relay",
		LastErrorHint:   "Check WAN and DNS.",
		LastLog:         []string{"initiating handshake", "lookup l.tunwg.com: i/o timeout"},
	})
	for _, want := range []string{"Can't reach", "Check WAN", "lookup l.tunwg.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error %q does not contain %q", err, want)
		}
	}
}
