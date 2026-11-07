package check

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/conditions"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// DNSResultWriter stores the last result of a DNS monitor, which the monitor's page shows.
type DNSResultWriter interface {
	SetDNSLastResult(ctx context.Context, monitorID int32, result string) error
}

// SetDNSResults sets where DNS results go. Call it before checks start.
func (c *Checker) SetDNSResults(w DNSResultWriter) { c.dnsResults = w }

// runDNS looks a record up on the monitor's resolver servers. It's UP if there's an answer, and
// the message is the same as the Node.js backend's. Conditions aren't supported yet.
func (c *Checker) runDNS(ctx context.Context, m monitor.Monitor) monitor.Result {
	hostname := strings.TrimSpace(m.Hostname)
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("server.address", hostname), attribute.String("dns.question.type", m.DNSResolveType))
	ctx, span := otel.Tracer(tracerName).Start(ctx, "dns.query")
	defer span.End()

	timeout := m.EffectiveTimeout()
	start := time.Now()
	servers, err := dnsServers(ctx, m.DNSResolveServer)
	if err != nil {
		return monitor.Result{Msg: requestError(err, timeout)}
	}
	answers, err := dnsLookup(ctx, servers, int(m.Port), hostname, m.DNSResolveType)
	if err != nil {
		// The last server's own timeout ends a moment before the check's, so both mean the same.
		if isTimeout(err) {
			return monitor.Result{Msg: fmt.Sprintf("timeout of %dms exceeded", timeout.Milliseconds())}
		}
		return monitor.Result{Msg: requestError(err, timeout)}
	}
	ping := elapsedMs(start)
	msg := dnsMessage(m.DNSResolveType, answers)
	if c.dnsResults != nil {
		if err := c.dnsResults.SetDNSLastResult(ctx, m.ID, monitor.SanitizeMsg(msg)); err != nil {
			slog.Warn("store DNS result", "monitor", m.ID, "error", err)
		}
	}
	nodes, err := conditions.Parse(m.Conditions)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	if len(nodes) > 0 {
		passed, err := conditions.Any(nodes, "record", dnsRecordValues(answers))
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		if !passed {
			// As in Node.js, the message is the records, and the monitor is DOWN.
			return monitor.Result{Msg: msg}
		}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: msg}
}

// dnsRecordValues are what conditions test, as in the Node.js backend: each address or name, each
// TXT string, the issuer of each CAA record that has one, the host of each MX record, the primary
// name server of SOA, and the target of each SRV record. A CNAME lookup tests its first answer.
func dnsRecordValues(answers []dns.RR) []string {
	var values []string
	for _, answer := range answers {
		switch record := answer.(type) {
		case *dns.A:
			values = append(values, record.A.String())
		case *dns.AAAA:
			values = append(values, record.AAAA.String())
		case *dns.PTR:
			values = append(values, dnsName(record.Ptr))
		case *dns.NS:
			values = append(values, dnsName(record.Ns))
		case *dns.TXT:
			values = append(values, record.Txt...)
		case *dns.CAA:
			if record.Tag == "issue" {
				values = append(values, record.Value)
			}
		case *dns.MX:
			values = append(values, dnsName(record.Mx))
		case *dns.SOA:
			return []string{dnsName(record.Ns)}
		case *dns.SRV:
			values = append(values, dnsName(record.Target))
		case *dns.CNAME:
			return []string{dnsName(record.Target)}
		}
	}
	return values
}

// dnsServers reads the comma-separated list. An IP address is used as it is, and a hostname,
// such as a container's, becomes all its addresses. One that doesn't resolve is left out, so
// that the others still work.
func dnsServers(ctx context.Context, list string) ([]string, error) {
	var servers []string
	for entry := range strings.SplitSeq(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if net.ParseIP(entry) != nil {
			servers = append(servers, entry)
			continue
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, entry)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			servers = append(servers, address.String())
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("None of the configured resolver servers could be resolved to an IP address") //nolint:staticcheck // Shown to users.
	}
	return servers, nil
}

// dnsLookup asks each server in turn until one gives an answer that isn't a server failure.
// Each gets an equal share of the time left, so that a dead server can't use it all.
func dnsLookup(ctx context.Context, servers []string, port int, hostname, recordType string) ([]dns.RR, error) {
	name := dns.Fqdn(hostname)
	question := dns.StringToType[recordType]
	if recordType == "PTR" {
		reverse, err := dns.ReverseAddr(hostname)
		if err != nil {
			return nil, fmt.Errorf("invalid IP address %q", hostname)
		}
		name = reverse
	}
	query := new(dns.Msg)
	query.SetQuestion(name, question)

	var lastErr error
	for i, server := range servers {
		var budget time.Duration
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline) / time.Duration(len(servers)-i)
		}
		response, err := dnsExchange(ctx, query, net.JoinHostPort(server, strconv.Itoa(port)), budget)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", server, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		switch response.Rcode {
		case dns.RcodeSuccess:
		case dns.RcodeNameError:
			return nil, fmt.Errorf("%s doesn't exist", hostname)
		default:
			lastErr = fmt.Errorf("%s answered %s", server, dns.RcodeToString[response.Rcode])
			continue
		}
		// An answer can start with the CNAME records that lead to the requested type.
		var answers []dns.RR
		for _, answer := range response.Answer {
			if answer.Header().Rrtype == question {
				answers = append(answers, answer)
			}
		}
		if len(answers) == 0 {
			return nil, fmt.Errorf("%s has no %s records", hostname, recordType)
		}
		return answers, nil
	}
	return nil, lastErr
}

// dnsExchange asks over UDP, and over TCP if the answer didn't fit.
func dnsExchange(ctx context.Context, query *dns.Msg, address string, timeout time.Duration) (*dns.Msg, error) {
	client := dns.Client{Net: "udp", Timeout: timeout}
	response, _, err := client.ExchangeContext(ctx, query, address)
	if err != nil {
		return nil, err
	}
	if response.Truncated {
		client.Net = "tcp"
		response, _, err = client.ExchangeContext(ctx, query, address)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

// dnsMessage formats answers as the Node.js backend does.
func dnsMessage(recordType string, answers []dns.RR) string {
	var parts []string
	for _, answer := range answers {
		switch record := answer.(type) {
		case *dns.A:
			parts = append(parts, record.A.String())
		case *dns.AAAA:
			parts = append(parts, record.AAAA.String())
		case *dns.PTR:
			parts = append(parts, dnsName(record.Ptr))
		case *dns.NS:
			parts = append(parts, dnsName(record.Ns))
		case *dns.TXT:
			parts = append(parts, strings.Join(record.Txt, ","))
		case *dns.CAA:
			if record.Tag == "issue" && record.Value != "" {
				parts = append(parts, record.Value)
			}
		case *dns.MX:
			parts = append(parts, fmt.Sprintf("Hostname: %s - Priority: %d", dnsName(record.Mx), record.Preference))
		case *dns.SRV:
			parts = append(parts, fmt.Sprintf("Name: %s | Port: %d | Priority: %d | Weight: %d",
				dnsName(record.Target), record.Port, record.Priority, record.Weight))
		case *dns.CNAME:
			return dnsName(record.Target)
		case *dns.SOA:
			return fmt.Sprintf("NS-Name: %s | Hostmaster: %s | Serial: %d | Refresh: %d | Retry: %d | Expire: %d | MinTTL: %d",
				dnsName(record.Ns), dnsName(record.Mbox), record.Serial, record.Refresh, record.Retry, record.Expire, record.Minttl)
		}
	}
	joined := strings.Join(parts, " | ")
	switch recordType {
	case "MX", "SRV":
		return joined
	case "NS":
		return "Servers: " + joined
	default:
		return "Records: " + joined
	}
}

func dnsName(name string) string {
	return strings.TrimSuffix(name, ".")
}
