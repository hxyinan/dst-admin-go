package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

func (s *Service) deliver(ctx context.Context, channel Channel, event Event) SendResult {
	if channel.Type == "email" {
		if err := s.mail.SendMail(ctx, channel.Recipients, event.Title, event.Message); err != nil {
			return SendResult{ErrorCode: "SMTP_SEND_FAILED"}
		}
		return SendResult{}
	}
	secret, err := s.store.Secret(channel.ID)
	if err != nil {
		return SendResult{ErrorCode: "CREDENTIALS_UNAVAILABLE"}
	}
	body, err := render(channel, event)
	if err != nil {
		return SendResult{ErrorCode: "TEMPLATE_INVALID"}
	}
	request, err := http.NewRequestWithContext(ctx, channel.Method, secret.URL, bytes.NewReader(body))
	if err != nil {
		return SendResult{ErrorCode: "WEBHOOK_REQUEST_INVALID"}
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if channel.Format == "text" {
		request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	for name, value := range secret.Headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("User-Agent", "DST-Admin-Notifications")
	request.Header.Set("X-DST-Event-ID", event.ID)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = webhookDial
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		var certificateError *tls.CertificateVerificationError
		var unknownAuthority x509.UnknownAuthorityError
		permanent := errors.Is(err, ErrInvalid) || errors.As(err, &certificateError) || errors.As(err, &unknownAuthority)
		return SendResult{ErrorCode: "WEBHOOK_CONNECTION_FAILED", Retryable: !permanent && !errors.Is(ctx.Err(), context.Canceled)}
	}
	defer response.Body.Close()
	result := SendResult{HTTPStatus: response.StatusCode}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCode = "WEBHOOK_HTTP_FAILED"
		result.Retryable = response.StatusCode == 429 || response.StatusCode >= 500
		return result
	}
	if channel.ResponseMode != "http" {
		data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if err != nil {
			return SendResult{HTTPStatus: response.StatusCode, ErrorCode: "WEBHOOK_RESPONSE_INVALID"}
		}
		var value map[string]json.RawMessage
		if json.Unmarshal(data, &value) != nil {
			return SendResult{HTTPStatus: response.StatusCode, ErrorCode: "WEBHOOK_RESPONSE_INVALID"}
		}
		key := "errcode"
		if channel.ResponseMode == "feishu" {
			key = "code"
		}
		var code int
		if json.Unmarshal(value[key], &code) != nil || code != 0 {
			result.ErrorCode = "WEBHOOK_REJECTED"
		}
	}
	return result
}

// Admin-configured LAN/loopback integrations are allowed. Reject link-local,
// multicast and unspecified addresses at connection time, including DNS results.
func webhookDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalid
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return nil, ErrInvalid
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error = ErrInvalid
	for _, item := range addresses {
		ip := item.IP
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		connection, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return connection, nil
		}
		last = err
	}
	return nil, last
}
