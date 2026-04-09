package dpi

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

func TestDetectionHTTPPorts_DefaultAndTargetSpecific(t *testing.T) {
	targets := []config.ProbeTarget{
		{Host: "a.example", Type: config.ProbeHTTPS, Port: 443},
		{Host: "a.example", Type: config.ProbeHTTP, Port: 8080},
		{Host: "b.example", Type: config.ProbeHTTP, Port: 8081},
	}
	ports := detectionHTTPPorts("a.example", targets)
	if len(ports) != 2 {
		t.Fatalf("expected ports [80,8080], got len=%d (%v)", len(ports), ports)
	}
	if ports[0] != 80 || ports[1] != 8080 {
		t.Fatalf("unexpected ports order/content: %v", ports)
	}
}

func TestClassifyHTTPErrorRefusedNotLikelyDPI(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	reason, conf, _, likelyDPI := classifyHTTPError(err)
	if reason != "http_unavailable" {
		t.Fatalf("reason=%s, want http_unavailable", reason)
	}
	if likelyDPI {
		t.Fatal("connection refused should not be marked as likely DPI")
	}
	if conf <= 0 {
		t.Fatalf("confidence=%f, want >0", conf)
	}
}

func TestClassifyTLSErrorCertificateIsNotDPI(t *testing.T) {
	bt, reason, conf, _ := classifyTLSError(errors.New("x509: certificate signed by unknown authority"), true)
	if bt != BlockNone {
		t.Fatalf("blocktype=%s, want none", bt.String())
	}
	if reason != "tls_certificate_error" {
		t.Fatalf("reason=%s, want tls_certificate_error", reason)
	}
	if conf < 0.8 {
		t.Fatalf("confidence=%f, want >= 0.8", conf)
	}
}

func TestClassifyTLSErrorTimeoutConfidenceDependsOnHTTP(t *testing.T) {
	timeoutErr := &net.OpError{Op: "read", Net: "tcp", Err: &timeoutNetError{}}

	_, _, confHTTP, _ := classifyTLSError(timeoutErr, true)
	_, _, confNoHTTP, _ := classifyTLSError(timeoutErr, false)
	if confHTTP <= confNoHTTP {
		t.Fatalf("expected higher confidence when HTTP passed: withHTTP=%f withoutHTTP=%f", confHTTP, confNoHTTP)
	}
}

type timeoutNetError struct{}

func (e *timeoutNetError) Error() string   { return "i/o timeout" }
func (e *timeoutNetError) Timeout() bool   { return true }
func (e *timeoutNetError) Temporary() bool { return true }

