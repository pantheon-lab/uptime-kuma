package check

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	jsonata "github.com/blues/jsonata-go"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/conditions"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// mqttBroker is the broker's address as paho takes it. The hostname may carry the mqtt.js
// scheme: mqtt:// (the default), mqtts://, ws:// or wss://, where a WebSocket path applies.
func mqttBroker(m monitor.Monitor) string {
	host := strings.TrimSpace(m.Hostname)
	scheme := "mqtt"
	if before, after, found := strings.Cut(host, "://"); found {
		scheme, host = strings.ToLower(before), after
	}
	switch scheme {
	case "mqtt", "tcp":
		scheme = "tcp"
	case "mqtts", "ssl", "tls":
		scheme = "ssl"
	}
	address := fmt.Sprintf("%s://%s:%d", scheme, host, m.Port)
	if scheme == "ws" || scheme == "wss" {
		path := m.MQTTWebsocketPath
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		address += path
	}
	return address
}

// runMQTT subscribes to the topic and waits for the first message, which a keyword, a JSON query
// or conditions then judge, as in the Node.js backend.
func runMQTT(ctx context.Context, m monitor.Monitor) monitor.Result {
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	options := paho.NewClientOptions().
		AddBroker(mqttBroker(m)).
		SetClientID("uptime-kuma_" + hex.EncodeToString(id)).
		SetUsername(m.MQTTUsername).
		SetPassword(m.MQTTPassword).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetCleanSession(true).
		SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	if deadline, ok := ctx.Deadline(); ok {
		options.SetConnectTimeout(time.Until(deadline))
	}
	client := paho.NewClient(options)
	connect := client.Connect()
	select {
	case <-connect.Done():
	case <-ctx.Done():
		return monitor.Result{Msg: "Timeout, Message not received"}
	}
	if err := connect.Error(); err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	defer client.Disconnect(0)

	type message struct{ topic, payload string }
	received := make(chan message, 1)
	subscribe := client.Subscribe(m.MQTTTopic, 0, func(_ paho.Client, msg paho.Message) {
		select {
		case received <- message{msg.Topic(), string(msg.Payload())}:
		default:
		}
	})
	select {
	case <-subscribe.Done():
		if err := subscribe.Error(); err != nil {
			return monitor.Result{Msg: "Cannot subscribe topic"}
		}
	case <-ctx.Done():
		return monitor.Result{Msg: "Timeout, Message not received"}
	}
	var got message
	select {
	case got = <-received:
	case <-ctx.Done():
		return monitor.Result{Msg: "Timeout, Message not received"}
	}
	return judgeMQTT(m, got.topic, got.payload)
}

func judgeMQTT(m monitor.Monitor, topic, payload string) monitor.Result {
	upMsg := fmt.Sprintf("Topic: %s; Message: %s", topic, payload)
	nodes, err := conditions.Parse(m.Conditions)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	if len(nodes) > 0 {
		jsonValue := ""
		if strings.TrimSpace(m.JSONPath) != "" {
			if value, err := mqttJSONValue(m.JSONPath, payload); err == nil && value != nil {
				if _, isUndefined := value.(undefined); !isUndefined {
					jsonValue = jsToString(value)
				}
			}
		}
		passed, err := conditions.Evaluate(nodes, map[string]string{"topic": topic, "message": payload, "json_value": jsonValue})
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		if !passed {
			return monitor.Result{Msg: fmt.Sprintf("Conditions not met - Topic: %s; Message: %s", topic, payload)}
		}
		return monitor.Result{OK: true, Msg: upMsg}
	}
	if m.MQTTCheckType == "json-query" {
		value, err := mqttJSONValue(m.JSONPath, payload)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		text := "undefined"
		if value != nil {
			text = jsToString(value)
		}
		if value != nil && text == m.ExpectedValue {
			return monitor.Result{OK: true, Msg: "Message received, expected value is found"}
		}
		return monitor.Result{Msg: "Message received but value is not equal to expected value, value was: [" + text + "]"}
	}
	if strings.Contains(payload, m.MQTTSuccessMessage) {
		return monitor.Result{OK: true, Msg: upMsg}
	}
	return monitor.Result{Msg: fmt.Sprintf("Message Mismatch - Topic: %s; Message: %s", m.MQTTTopic, payload)}
}

func mqttJSONValue(path, payload string) (any, error) {
	var data any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return nil, fmt.Errorf("the message isn't JSON: %w", err)
	}
	expr, err := jsonata.Compile(path)
	if err != nil {
		return nil, err
	}
	value, err := expr.Eval(data)
	if errors.Is(err, jsonata.ErrUndefined) {
		return undefined{}, nil
	}
	return value, err
}

// runRabbitMQ asks each node's management API for alarms, until one answers without any.
func (c *Checker) runRabbitMQ(ctx context.Context, m monitor.Monitor) monitor.Result {
	transport := c.verified
	if m.IgnoreTLS {
		transport = c.insecure
	}
	client := &http.Client{Transport: transport}
	var errs []string
	for i, node := range m.RabbitMQNodes {
		err := rabbitMQNode(ctx, client, m, node)
		if err == nil {
			msg := "Node is reachable and there are no alerts in the cluster"
			if len(m.RabbitMQNodes) > 1 {
				msg = fmt.Sprintf("One of the %d nodes is reachable and there are no alerts in the cluster", len(m.RabbitMQNodes))
			}
			return monitor.Result{OK: true, Msg: msg}
		}
		errs = append(errs, fmt.Sprintf("Node %d: %s", i+1, err))
		if ctx.Err() != nil {
			break
		}
	}
	return monitor.Result{Msg: fmt.Sprintf("All %d nodes failed because %s", len(errs), strings.Join(errs, "; "))}
}

func rabbitMQNode(ctx context.Context, client *http.Client, m monitor.Monitor, node string) error {
	base, err := url.Parse(strings.TrimSuffix(node, "/") + "/")
	if err != nil {
		return err
	}
	target := base.JoinPath("api/health/checks/alarms/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(m.RabbitMQUsername, m.RabbitMQPassword)
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("Request timed out") //nolint:staticcheck // As in Node.js.
		}
		return errors.New(requestError(err, m.EffectiveTimeout()))
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusServiceUnavailable:
		var alarm struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &alarm)
		return errors.New(alarm.Reason)
	}
	return fmt.Errorf("%d - %s", res.StatusCode, http.StatusText(res.StatusCode))
}

// runKafka sends the message to the topic with no retries, as the Node.js backend's producer did.
func runKafka(ctx context.Context, m monitor.Monitor) monitor.Result {
	opts := []kgo.Opt{
		kgo.SeedBrokers(m.KafkaBrokers...),
		kgo.ClientID("Uptime-Kuma"),
		kgo.RequestRetries(0),
		kgo.RecordRetries(1),
		kgo.UnknownTopicRetries(0),
		kgo.ProducerLinger(0),
		// As kafkajs: idempotent writes need a producer ID from a coordinator first.
		kgo.DisableIdempotentWrite(),
		kgo.DialTimeout(m.EffectiveTimeout()),
	}
	if m.KafkaAllowAutoTopicCreation {
		// A topic made on the first request takes a moment to be ready.
		opts = append(opts, kgo.AllowAutoTopicCreation(), kgo.UnknownTopicRetries(5))
	}
	if m.KafkaSSL {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	if mechanism := kafkaSASL(m.KafkaSASL); mechanism != nil {
		opts = append(opts, kgo.SASL(mechanism))
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return monitor.Result{Msg: "Error in producer connection: " + err.Error()}
	}
	defer client.Close()
	start := time.Now()
	if err := client.Ping(ctx); err != nil {
		return monitor.Result{Msg: "Error in producer connection: " + kafkaError(ctx, err, m)}
	}
	if err := client.ProduceSync(ctx, &kgo.Record{Topic: m.KafkaTopic, Value: []byte(m.KafkaMessage)}).FirstErr(); err != nil {
		return monitor.Result{Msg: "Error sending message: " + kafkaError(ctx, err, m)}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: "Message sent successfully"}
}

func kafkaSASL(s *monitor.KafkaSASL) sasl.Mechanism {
	if s == nil {
		return nil
	}
	switch s.Mechanism {
	case "plain":
		return plain.Auth{User: s.Username, Pass: s.Password, Zid: s.AuthorizationIdentity}.AsMechanism()
	case "scram-sha-256":
		return scram.Auth{User: s.Username, Pass: s.Password}.AsSha256Mechanism()
	case "scram-sha-512":
		return scram.Auth{User: s.Username, Pass: s.Password}.AsSha512Mechanism()
	}
	return nil
}

func kafkaError(ctx context.Context, err error, m monitor.Monitor) string {
	if ctx.Err() != nil {
		return "Timeout"
	}
	msg := err.Error()
	if m.KafkaSASL != nil && m.KafkaSASL.Password != "" {
		msg = strings.ReplaceAll(msg, m.KafkaSASL.Password, "***")
	}
	return msg
}
