package check

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/miekg/dns"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

type dnsHandler func(w dns.ResponseWriter, r *dns.Msg)

func (h dnsHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) { h(w, r) }

// startResolver answers DNS queries on a loopback address, and returns the address and
// the port it listens on. The port is the monitor's, so two resolvers sharing a port
// must listen on different loopback addresses.
func startResolver(t *testing.T, address string, h dns.Handler) (string, int32) {
	t.Helper()
	conn, err := net.ListenPacket("udp", address+":0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: h}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = server.Shutdown()
		_ = conn.Close()
	})
	host, portText, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, int32(port)
}

func reply(w dns.ResponseWriter, r *dns.Msg, rcode int, answers ...dns.RR) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = rcode
	m.Answer = answers
	_ = w.WriteMsg(m)
}

func aRecord(name string, ip string) *dns.A {
	return &dns.A{Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Ttl: 60}, A: net.ParseIP(ip)}
}

func dnsMonitorType(servers string, port int32, hostname, recordType string) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.Hostname = "records", monitor.TypeDNS, hostname
	m.DNSResolveServer, m.Port, m.DNSResolveType = servers, port, recordType
	m.Timeout = 2
	return m
}

// newChecker closes itself when the test ends.
func newChecker(t *testing.T) *Checker {
	t.Helper()
	checker := New(nil)
	t.Cleanup(checker.Close)
	return checker
}

// recordingWriter keeps what the monitor's page would show.
type recordingWriter struct{ results []string }

func (w *recordingWriter) SetDNSLastResult(_ context.Context, _ int32, result string) error {
	w.results = append(w.results, result)
	return nil
}

func TestDNSLooksUpRecords(t *testing.T) {
	// The handler runs on the resolver's goroutine, so its notes are guarded.
	var (
		mu    sync.Mutex
		asked []string
	)
	host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
		mu.Lock()
		asked = append(asked, r.Question[0].Name+" "+dns.TypeToString[r.Question[0].Qtype])
		mu.Unlock()
		reply(w, r, dns.RcodeSuccess,
			&dns.CNAME{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeCNAME, Ttl: 60}, Target: "real.example.com."},
			aRecord("real.example.com.", "1.2.3.4"),
			aRecord("real.example.com.", "5.6.7.8"),
		)
	}))
	writer := &recordingWriter{}
	c := newChecker(t)
	c.SetDNSResults(writer)

	m := dnsMonitorType(host, port, "example.com", "A")
	r := c.Run(context.Background(), m)
	if !r.OK || r.Ping == nil {
		t.Fatalf("result: %+v", r)
	}
	// The CNAME that leads to the answer isn't part of it, as in the Node.js backend.
	if r.Msg != "Records: 1.2.3.4 | 5.6.7.8" {
		t.Errorf("message: %q", r.Msg)
	}
	mu.Lock()
	gotAsked := slices.Clone(asked)
	mu.Unlock()
	if len(gotAsked) != 1 || gotAsked[0] != "example.com. A" {
		t.Errorf("question asked: %v", gotAsked)
	}
	if len(writer.results) != 1 || writer.results[0] != r.Msg {
		t.Errorf("stored result: %v", writer.results)
	}
}

func TestDNSMessageFormats(t *testing.T) {
	cases := map[string]struct {
		hostname string
		records  []dns.RR
		want     string
	}{
		"A": {
			hostname: "example.com",
			records:  []dns.RR{aRecord("example.com.", "1.2.3.4")},
			want:     "Records: 1.2.3.4",
		},
		"AAAA": {
			hostname: "example.com",
			records:  []dns.RR{&dns.AAAA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeAAAA, Ttl: 60}, AAAA: net.ParseIP("2001:db8::1")}},
			want:     "Records: 2001:db8::1",
		},
		"MX": {
			hostname: "example.com",
			records:  []dns.RR{&dns.MX{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeMX, Ttl: 60}, Preference: 10, Mx: "mail.example.com."}},
			want:     "Hostname: mail.example.com - Priority: 10",
		},
		"NS": {
			hostname: "example.com",
			records:  []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeNS, Ttl: 60}, Ns: "ns1.example.com."}},
			want:     "Servers: ns1.example.com",
		},
		"TXT": {
			hostname: "example.com",
			records:  []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeTXT, Ttl: 60}, Txt: []string{"v=spf1", "-all"}}},
			want:     "Records: v=spf1,-all",
		},
		"PTR": {
			hostname: "10.0.0.1",
			records:  []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: "1.0.0.10.in-addr.arpa.", Rrtype: dns.TypePTR, Ttl: 60}, Ptr: "host.example.com."}},
			want:     "Records: host.example.com",
		},
		"SRV": {
			hostname: "_sip._tcp.example.com",
			records: []dns.RR{&dns.SRV{
				Hdr:      dns.RR_Header{Name: "_sip._tcp.example.com.", Rrtype: dns.TypeSRV, Ttl: 60},
				Priority: 5, Weight: 0, Port: 5060, Target: "sip.example.com.",
			}},
			want: "Name: sip.example.com | Port: 5060 | Priority: 5 | Weight: 0",
		},
		"CNAME": {
			hostname: "example.com",
			records:  []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeCNAME, Ttl: 60}, Target: "other.example.net."}},
			want:     "other.example.net",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
				reply(w, r, dns.RcodeSuccess, tc.records...)
			}))
			m := dnsMonitorType(host, port, tc.hostname, name)
			if r := newChecker(t).Run(context.Background(), m); !r.OK || r.Msg != tc.want {
				t.Errorf("got %+v, want message %q", r, tc.want)
			}
		})
	}
}

func TestDNSFailures(t *testing.T) {
	t.Run("the name doesn't exist", func(t *testing.T) {
		host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
			reply(w, r, dns.RcodeNameError)
		}))
		m := dnsMonitorType(host, port, "gone.example.com", "A")
		if r := newChecker(t).Run(context.Background(), m); r.OK || r.Msg != "gone.example.com doesn't exist" {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("no records of that type", func(t *testing.T) {
		host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
			reply(w, r, dns.RcodeSuccess, aRecord("example.com.", "1.2.3.4"))
		}))
		m := dnsMonitorType(host, port, "example.com", "TXT")
		if r := newChecker(t).Run(context.Background(), m); r.OK || r.Msg != "example.com has no TXT records" {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("the first server fails and the second answers", func(t *testing.T) {
		failing, _ := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
			reply(w, r, dns.RcodeServerFailure)
		}))
		working, port := startResolver(t, "127.0.0.2", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
			reply(w, r, dns.RcodeSuccess, aRecord("example.com.", "1.2.3.4"))
		}))
		if failing == working {
			t.Fatal("both resolvers bound the same address")
		}
		m := dnsMonitorType(failing+", "+working, port, "example.com", "A")
		if r := newChecker(t).Run(context.Background(), m); !r.OK || r.Msg != "Records: 1.2.3.4" {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("every server fails", func(t *testing.T) {
		host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
			reply(w, r, dns.RcodeServerFailure)
		}))
		m := dnsMonitorType(host, port, "example.com", "A")
		if r := newChecker(t).Run(context.Background(), m); r.OK || !strings.Contains(r.Msg, "answered SERVFAIL") {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("a server that never answers", func(t *testing.T) {
		_, port := startResolver(t, "127.0.0.1", dnsHandler(func(dns.ResponseWriter, *dns.Msg) {}))
		m := dnsMonitorType("127.0.0.1", port, "example.com", "A")
		m.Timeout = 1
		r := newChecker(t).Run(context.Background(), m)
		if r.OK || r.Msg != "timeout of 1000ms exceeded" {
			t.Errorf("got %+v", r)
		}
	})
}

func TestDNSServers(t *testing.T) {
	ctx := context.Background()
	servers, err := dnsServers(ctx, "1.1.1.1, 8.8.8.8, ,")
	if err != nil || len(servers) != 2 || servers[0] != "1.1.1.1" || servers[1] != "8.8.8.8" {
		t.Errorf("addresses: %v %v", servers, err)
	}
	// A hostname, such as a container's, becomes its addresses.
	servers, err = dnsServers(ctx, "localhost")
	if err != nil || len(servers) == 0 {
		t.Errorf("localhost: %v %v", servers, err)
	}
	if _, err := dnsServers(ctx, " , "); err == nil {
		t.Error("an empty list should fail")
	}
}
