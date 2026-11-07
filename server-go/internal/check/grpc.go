package check

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bufbuild/protocompile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const grpcProtoFile = "monitor.proto"

// runGRPC calls one unary method, described by the monitor's .proto text, and looks for the
// keyword in the response as JSON, as the Node.js backend did.
func runGRPC(ctx context.Context, m monitor.Monitor) monitor.Result {
	method, err := grpcMethod(ctx, m.GRPCProtobuf, strings.TrimSpace(m.GRPCServiceName), strings.TrimSpace(m.GRPCMethod))
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	request := dynamicpb.NewMessage(method.Input())
	body := strings.TrimSpace(m.GRPCBody)
	if body == "" {
		body = "{}"
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(body), request); err != nil {
		return monitor.Result{Msg: "invalid body: " + err.Error()}
	}
	if raw := strings.TrimSpace(m.GRPCMetadata); raw != "" {
		var pairs map[string]string
		if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
			return monitor.Result{Msg: "invalid metadata: " + err.Error()}
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(pairs))
	}

	transport := insecure.NewCredentials()
	if m.GRPCEnableTLS {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	start := time.Now()
	conn, err := grpc.NewClient(strings.TrimSpace(m.GRPCURL), grpc.WithTransportCredentials(transport))
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	defer func() { _ = conn.Close() }()
	service := method.Parent().(protoreflect.ServiceDescriptor)
	path := fmt.Sprintf("/%s/%s", service.FullName(), method.Name())
	response := dynamicpb.NewMessage(method.Output())

	var text string
	if err := conn.Invoke(ctx, path, request, response); err != nil {
		code := status.Code(err)
		if code != codes.Canceled {
			if ctx.Err() != nil {
				return monitor.Result{Msg: fmt.Sprintf("timeout of %dms exceeded", m.EffectiveTimeout().Milliseconds())}
			}
			return monitor.Result{Msg: grpcError(err)}
		}
		// The Node.js backend counted CANCELLED as a response.
		text = fmt.Sprintf("%d is considered OK because %s", code, status.Convert(err).Message())
	} else {
		encoded, err := protojson.Marshal(response)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		text = compactJSON(encoded)
	}
	ping := elapsedMs(start)

	found := strings.Contains(text, m.Keyword)
	if found != !m.InvertKeyword {
		state := "not"
		if found {
			state = "present"
		}
		shown := text
		if len(shown) > 50 {
			shown = truncateRunes(shown, 47) + "..."
		}
		return monitor.Result{Msg: fmt.Sprintf("keyword [%s] is %s in [%s]", m.Keyword, state, shown)}
	}
	state := "not"
	if found {
		state = "is"
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("%s, keyword [%s] %s found", text, m.Keyword, state)}
}

// grpcMethod compiles the .proto text and finds the method. A service can be named without its
// package, and a method in lower camel case, as the Node.js backend allowed.
func grpcMethod(ctx context.Context, proto, serviceName, methodName string) (protoreflect.MethodDescriptor, error) {
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{grpcProtoFile: proto}),
		}),
	}
	files, err := compiler.Compile(ctx, grpcProtoFile)
	if err != nil {
		return nil, fmt.Errorf("invalid protobuf: %w", err)
	}
	var service protoreflect.ServiceDescriptor
	services := files[0].Services()
	for i := range services.Len() {
		candidate := services.Get(i)
		if string(candidate.FullName()) == serviceName || string(candidate.Name()) == serviceName {
			service = candidate
			break
		}
	}
	if service == nil {
		return nil, fmt.Errorf("no such service: %s", serviceName)
	}
	methods := service.Methods()
	for i := range methods.Len() {
		candidate := methods.Get(i)
		name := string(candidate.Name())
		if name == methodName || lowerFirst(name) == methodName {
			if candidate.IsStreamingClient() || candidate.IsStreamingServer() {
				return nil, fmt.Errorf("%s is a streaming method; only unary methods can be checked", name)
			}
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("%s has no method %s", service.FullName(), methodName)
}

func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[size:]
}

// grpcError formats an error as the Node.js client did, such as "14 UNAVAILABLE: ...".
func grpcError(err error) string {
	s, isStatus := status.FromError(err)
	if !isStatus {
		return err.Error()
	}
	var name strings.Builder
	for i, r := range s.Code().String() {
		if i > 0 && unicode.IsUpper(r) {
			name.WriteByte('_')
		}
		name.WriteRune(unicode.ToUpper(r))
	}
	return fmt.Sprintf("%d %s: %s", s.Code(), name.String(), s.Message())
}

// compactJSON removes the spaces protojson adds at random, so the text is stable. Fields stay
// in the order of the .proto file.
func compactJSON(data []byte) string {
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		return string(data)
	}
	return out.String()
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
