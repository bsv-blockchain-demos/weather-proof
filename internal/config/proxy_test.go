package config_test

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
)

// recordingHandler is a minimal slog.Handler that stores every record it
// receives, so tests can count and inspect log output precisely.
type recordingHandler struct {
	records *[]slog.Record
}

func newRecordingHandler() (*recordingHandler, *[]slog.Record) {
	records := &[]slog.Record{}
	return &recordingHandler{records: records}, records
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *recordingHandler) WithGroup(_ string) slog.Handler { return h }

func TestParseCIDRListAcceptsIPv4AndIPv6Prefixes(t *testing.T) {
	tp, err := config.ParseCIDRList([]string{"10.0.0.0/8", "2001:db8::/32"})
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v, want nil", err)
	}
	if tp.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", tp.Len())
	}
}

func TestParseCIDRListRejectsAMalformedEntry(t *testing.T) {
	_, err := config.ParseCIDRList([]string{"10.0.0.0/8", "10.0.0.0/99"})
	if err == nil {
		t.Fatal("ParseCIDRList() error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Errorf("ParseCIDRList() = %q, want it to name TRUSTED_PROXY_CIDRS", err.Error())
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("ParseCIDRList() = %q, want it to name index 1", err.Error())
	}
	if strings.Contains(err.Error(), "99") {
		t.Errorf("ParseCIDRList() = %q, must not echo the entry's text", err.Error())
	}
}

func TestParseCIDRListRejectsABareAddress(t *testing.T) {
	_, err := config.ParseCIDRList([]string{"10.0.0.1"})
	if err == nil {
		t.Fatal("ParseCIDRList() error = nil, want an error for a bare address")
	}
}

func TestParseCIDRListAcceptsAnEmptyList(t *testing.T) {
	tp, err := config.ParseCIDRList(nil)
	if err != nil {
		t.Fatalf("ParseCIDRList(nil) error = %v, want nil", err)
	}
	if tp.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", tp.Len())
	}

	tp2, err := config.ParseCIDRList([]string{})
	if err != nil {
		t.Fatalf("ParseCIDRList([]) error = %v, want nil", err)
	}
	if tp2.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", tp2.Len())
	}
}

func TestContainsMatchesAFourInSixMappedPeer(t *testing.T) {
	tp, err := config.ParseCIDRList([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v", err)
	}
	addr := netip.MustParseAddr("::ffff:10.1.2.3")
	if !tp.Contains(addr) {
		t.Error("Contains() = false, want true for a 4-in-6 mapped peer inside the trusted prefix")
	}
}

func TestContainsRejectsAnAddressOutsideEveryPrefix(t *testing.T) {
	tp, err := config.ParseCIDRList([]string{"10.0.0.0/8", "192.168.0.0/16"})
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v", err)
	}
	addr := netip.MustParseAddr("172.16.0.1")
	if tp.Contains(addr) {
		t.Error("Contains() = true, want false for an address outside every prefix")
	}
}

func TestContainsIsFalseForAnEmptyList(t *testing.T) {
	tp, err := config.ParseCIDRList(nil)
	if err != nil {
		t.Fatalf("ParseCIDRList(nil) error = %v", err)
	}
	if tp.Contains(netip.MustParseAddr("10.1.2.3")) {
		t.Error("Contains() = true, want false: trust nothing means trust nothing, including RFC1918")
	}
}

func TestContainsMatchesIPv6WithinItsPrefix(t *testing.T) {
	tp, err := config.ParseCIDRList([]string{"2001:db8::/32"})
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v", err)
	}
	if !tp.Contains(netip.MustParseAddr("2001:db8::1")) {
		t.Error("Contains() = false, want true for an address inside the IPv6 prefix")
	}
	if tp.Contains(netip.MustParseAddr("2001:db9::1")) {
		t.Error("Contains() = true, want false for an address outside the IPv6 prefix")
	}
}

func TestLogBootStateEmitsExactlyOneWarnWhenEmpty(t *testing.T) {
	tp, err := config.ParseCIDRList(nil)
	if err != nil {
		t.Fatalf("ParseCIDRList(nil) error = %v", err)
	}
	handler, records := newRecordingHandler()
	log := slog.New(handler)

	tp.LogBootState(log)

	if len(*records) != 1 {
		t.Fatalf("len(records) = %d, want exactly 1", len(*records))
	}
	rec := (*records)[0]
	if rec.Level != slog.LevelWarn {
		t.Errorf("record level = %v, want WARN", rec.Level)
	}
	want := "TRUSTED_PROXY_CIDRS empty: forwarding headers ignored, rate limiting is per-proxy-pod and therefore effectively global"
	if rec.Message != want {
		t.Errorf("record message = %q, want %q", rec.Message, want)
	}
}

func TestLogBootStateIsSilentWhenNonEmpty(t *testing.T) {
	tp, err := config.ParseCIDRList([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRList() error = %v", err)
	}
	handler, records := newRecordingHandler()
	log := slog.New(handler)

	tp.LogBootState(log)

	if len(*records) != 0 {
		t.Fatalf("len(records) = %d, want exactly 0", len(*records))
	}
}
