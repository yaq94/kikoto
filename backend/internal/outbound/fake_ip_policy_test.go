package outbound

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
	"time"
)

// TestTransparentResolverPlaceholderIsAcceptedForHostnames keeps media playback
// working on hosts whose local transparent resolver answers every foreign name
// from its fake-IP pool. The connection still goes to the resolved address, and
// every other private or reserved range keeps its rejection.
func TestTransparentResolverPlaceholderIsAcceptedForHostnames(t *testing.T) {
	var dialed string
	policy, err := NewPolicy([]Destination{{URL: "https://api.source.test", AllowPrivate: true}}, Options{
		AllowPublicOrigins: true,
		Resolver: resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("198.18.0.107")}}, nil
		}),
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = address
			return stubConnection(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mediaURL, _ := url.Parse("https://media.storage.test/files/track.mp3")
	if err := policy.ValidateURL(mediaURL); err != nil {
		t.Fatalf("public media host rejected before address validation: %v", err)
	}
	if _, err = policy.dial(context.Background(), "tcp", "media.storage.test:443"); err != nil {
		t.Fatalf("hostname answered from the local fake-IP pool was rejected: %v", err)
	}
	if dialed != "198.18.0.107:443" {
		t.Fatalf("dialed %q, want the resolved placeholder address", dialed)
	}
}

func TestTransparentResolverPlaceholderDoesNotExcuseLiteralsOrOtherRanges(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		host   string
		answer string
	}{
		{"fake-IP literal", "198.18.0.107", "198.18.0.107"},
		{"private answer", "media.storage.test", "10.1.2.3"},
		{"loopback answer", "media.storage.test", "127.0.0.1"},
		{"documentation answer", "media.storage.test", "192.0.2.10"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			policy, err := NewPolicy([]Destination{{URL: "https://api.source.test", AllowPrivate: true}}, Options{
				AllowPublicOrigins: true,
				Resolver: resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP(scenario.answer)}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = policy.dial(context.Background(), "tcp", scenario.host+":443"); err == nil || !errors.Is(err, ErrPolicyViolation) {
				t.Fatalf("dial error = %v, want policy violation", err)
			}
		})
	}
}

func TestTransparentResolverPlaceholderKeepsConfiguredPrivateOriginException(t *testing.T) {
	var dialed string
	policy, err := NewPolicy([]Destination{{URL: "https://lan.source.test", AllowPrivate: true}}, Options{
		Resolver: resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.168.5.5")}}, nil
		}),
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = address
			return stubConnection(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.dial(context.Background(), "tcp", "lan.source.test:443"); err != nil {
		t.Fatalf("configured private origin was rejected: %v", err)
	}
	if dialed != "192.168.5.5:443" {
		t.Fatalf("dialed %q", dialed)
	}
}

// stubConnection satisfies net.Conn for policy dial tests that never transfer.
type stubConn struct{}

func stubConnection() net.Conn { return stubConn{} }

func (stubConn) Read([]byte) (int, error)         { return 0, errors.New("synthetic") }
func (stubConn) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (stubConn) Close() error                     { return nil }
func (stubConn) LocalAddr() net.Addr              { return stubAddress("local") }
func (stubConn) RemoteAddr() net.Addr             { return stubAddress("remote") }
func (stubConn) SetDeadline(time.Time) error      { return nil }
func (stubConn) SetReadDeadline(time.Time) error  { return nil }
func (stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddress string

func (address stubAddress) Network() string { return "stub" }
func (address stubAddress) String() string  { return string(address) }
