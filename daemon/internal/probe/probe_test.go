package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/frlvmxm-droid/darkroute/daemon/internal/config"
)

// -- classifyErr tests --------------------------------------------------------

func TestClassifyErr_Nil(t *testing.T) {
	if got := classifyErr(nil); got != ProbeErrNone {
		t.Fatalf("nil error want ProbeErrNone, got %s", got)
	}
}

func TestClassifyErr_DNS(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "example.invalid", IsNotFound: true}
	if got := classifyErr(dnsErr); got != ProbeErrDNS {
		t.Fatalf("dns error want ProbeErrDNS, got %s", got)
	}
}

func TestClassifyErr_Timeout(t *testing.T) {
	// Build a net.Error that reports Timeout().
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	err := ctx.Err() // context.DeadlineExceeded — classified as Other since it's not net.Error
	// Wrap it in a net.OpError with timeout flag.
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &timeoutError{},
	}
	if got := classifyErr(opErr); got != ProbeErrTimeout {
		t.Fatalf("timeout error want ProbeErrTimeout, got %s; (plain ctx err: %s)", got, classifyErr(err))
	}
}

// timeoutError is a net.Error that signals Timeout.
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestClassifyErr_RST(t *testing.T) {
	opErr := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: syscall.ECONNRESET,
	}
	if got := classifyErr(opErr); got != ProbeErrRST {
		t.Fatalf("ECONNRESET want ProbeErrRST, got %s", got)
	}
}

func TestClassifyErr_TLS(t *testing.T) {
	for _, msg := range []string{
		"tls: bad certificate",
		"handshake failure",
		"x509: certificate signed by unknown authority",
	} {
		err := errors.New(msg)
		if got := classifyErr(err); got != ProbeErrTLSHandshake {
			t.Fatalf("msg=%q want ProbeErrTLSHandshake, got %s", msg, got)
		}
	}
}

func TestClassifyErr_HTTPReject(t *testing.T) {
	for _, msg := range []string{"EOF", "connection reset by peer", "broken pipe"} {
		err := errors.New(msg)
		if got := classifyErr(err); got != ProbeErrHTTPReject {
			t.Fatalf("msg=%q want ProbeErrHTTPReject, got %s", msg, got)
		}
	}
}

func TestClassifyErr_Other(t *testing.T) {
	err := fmt.Errorf("something completely different")
	if got := classifyErr(err); got != ProbeErrOther {
		t.Fatalf("unknown error want ProbeErrOther, got %s", got)
	}
}

// -- ProbeErrType.String -------------------------------------------------------

func TestErrTypeString(t *testing.T) {
	cases := map[ProbeErrType]string{
		ProbeErrNone:         "none",
		ProbeErrTimeout:      "timeout",
		ProbeErrRST:          "rst",
		ProbeErrTLSHandshake: "tls_handshake",
		ProbeErrHTTPReject:   "http_reject",
		ProbeErrDNS:          "dns",
		ProbeErrOther:        "other",
	}
	for et, want := range cases {
		if got := et.String(); got != want {
			t.Errorf("ProbeErrType(%d).String() = %q, want %q", et, got, want)
		}
	}
}

// -- aggregate tests ----------------------------------------------------------

func TestAggregateAllSuccess(t *testing.T) {
	results := []Result{
		{Success: true, RTT: 100 * time.Millisecond},
		{Success: true, RTT: 200 * time.Millisecond},
	}
	agg := aggregate(results)
	if !agg.Success {
		t.Fatal("want success=true")
	}
	if agg.PacketLoss != 0 {
		t.Fatalf("want loss=0, got %f", agg.PacketLoss)
	}
	if agg.AvgRTT != 150*time.Millisecond {
		t.Fatalf("want avgRTT=150ms, got %v", agg.AvgRTT)
	}
	if agg.DominantErrType != ProbeErrNone {
		t.Fatalf("want dominant=none, got %s", agg.DominantErrType)
	}
}

func TestAggregateAllFail(t *testing.T) {
	results := []Result{
		{Success: false, ErrType: ProbeErrTimeout},
		{Success: false, ErrType: ProbeErrTimeout},
		{Success: false, ErrType: ProbeErrRST},
	}
	agg := aggregate(results)
	if agg.Success {
		t.Fatal("want success=false when all fail")
	}
	if agg.PacketLoss != 1.0 {
		t.Fatalf("want loss=1.0, got %f", agg.PacketLoss)
	}
	if agg.DominantErrType != ProbeErrTimeout {
		t.Fatalf("want dominant=timeout, got %s", agg.DominantErrType)
	}
}

func TestAggregatePartialFail(t *testing.T) {
	// 2 success, 1 failure → success (>50%) but loss=0.333.
	results := []Result{
		{Success: true, RTT: 100 * time.Millisecond},
		{Success: true, RTT: 300 * time.Millisecond},
		{Success: false, ErrType: ProbeErrRST},
	}
	agg := aggregate(results)
	if !agg.Success {
		t.Fatal("want success=true with majority passing")
	}
	wantLoss := 1.0 / 3.0
	if agg.PacketLoss < wantLoss-0.01 || agg.PacketLoss > wantLoss+0.01 {
		t.Fatalf("want loss≈%.3f, got %f", wantLoss, agg.PacketLoss)
	}
}

func TestAggregateEmpty(t *testing.T) {
	agg := aggregate(nil)
	if agg.Success {
		t.Fatal("want success=false for empty results")
	}
}

// -- Engine.ProbeTargets with empty target list --------------------------------

func TestProbeTargetsEmpty(t *testing.T) {
	cfg := config.DefaultDaemonConfig()
	e := New(cfg)
	agg := e.ProbeTargets(context.Background(), nil, 0)
	if agg.Success {
		t.Fatal("empty targets: want success=false")
	}
}
