package manage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"time"
)

// This deliberately uses an ordinary TLS client on a separate connection.
// It isolates basic reachability/trust/ALPN from the actual helper's fingerprint,
// authentication and sustained traffic. A successful probe never marks a tunnel OK.
func ctProbeHTTPS(parent context.Context, spec TunnelSpec) string {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	endpoint, serverName, caFile := spec.XrayClient.Server, spec.XrayClient.ServerName, spec.XrayClient.CAFile
	if spec.NaiveClient.Enabled() {
		endpoint, caFile = spec.NaiveClient.Server, spec.NaiveClient.CAFile
		serverName, _, _ = net.SplitHostPort(endpoint) // Naive uses the IP endpoint, without a DNS SNI
	}
	result := "tcp-failed"
	for ctx.Err() == nil {
		// The Iran engine starts asynchronously. Its port reservation can
		// briefly accept TCP before the TLS helper owns the listener. Retry
		// startup EOF/refusal under the same total budget, without skipping it.
		attempt, stopAttempt := context.WithTimeout(ctx, time.Second)
		conn, err := (&net.Dialer{}).DialContext(attempt, "tcp", endpoint)
		if err == nil {
			if spec.XrayClient.Mode == "reality" {
				conn.Close()
				stopAttempt()
				return "tcp-ok" // only authenticated tunnel traffic can prove REALITY
			}
			result = "tls-failed"
			ca, caErr := os.ReadFile(caFile)
			roots := x509.NewCertPool()
			if caErr != nil || !roots.AppendCertsFromPEM(ca) {
				conn.Close()
				stopAttempt()
				return result
			}
			stopClose := context.AfterFunc(attempt, func() { conn.Close() })
			client := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: roots, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS12})
			err = client.HandshakeContext(attempt)
			conn.Close()
			stopClose()
			stopAttempt()
			if err == nil {
				if client.ConnectionState().NegotiatedProtocol == "h2" {
					return "tls-ok"
				}
				return result
			}
			var verification *tls.CertificateVerificationError
			if errors.As(err, &verification) {
				return result
			}
		} else {
			stopAttempt()
		}
		ctSleep(ctx, 100*time.Millisecond)
	}
	return result
}

func ctValidHTTPSProbe(tr, probe string) bool {
	if !managedTransport(tr) {
		return false
	}
	switch probe {
	case "tcp-failed":
		return true
	case "tcp-ok":
		return tr == "reality"
	case "tls-ok", "tls-failed":
		return tr == "naive" || tr == "xhttp"
	}
	return false
}

func ctHTTPSProbeLabel(probe string) string {
	switch probe {
	case "tcp-failed":
		return "public TCP connection failed"
	case "tcp-ok":
		return "public TCP reachable; REALITY authentication requires tunnel traffic"
	case "tls-failed":
		return "TCP reachable; certificate/TLS/HTTP2 probe failed"
	case "tls-ok":
		return "TCP + verified TLS/HTTP2 passed; this is not a tunnel traffic check"
	}
	return "not reported by peer"
}

func ctHTTPSResult(r ConnTestResult, c *connTestCase) ConnTestResult {
	if c.kind == "reverse" && managedTransport(c.tr) {
		r.HTTPSProbe = c.httpsProbe
		r.TestPort, _ = strconv.Atoi(c.link.Port)
	}
	return r
}
