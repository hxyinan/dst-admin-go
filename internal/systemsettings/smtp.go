package systemsettings

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func (s *Service) TestSMTP(ctx context.Context, input SMTPTestInput) (SMTPTestResult, error) {
	return s.withSMTP(ctx, input, nil)
}

// SendMail uses the current SMTP settings without restarting the Controller.
func (s *Service) SendMail(ctx context.Context, recipients []string, subject, body string) error {
	runtime, err := s.Runtime()
	if err != nil {
		return err
	}
	from, err := mail.ParseAddress(runtime.SenderEmail)
	if err != nil || len(recipients) == 0 || len(recipients) > 20 || strings.ContainsAny(subject+runtime.SenderEmail, "\r\n\x00") {
		return ErrInvalidInput
	}
	addresses := make([]string, 0, len(recipients))
	for _, value := range recipients {
		address, err := mail.ParseAddress(value)
		if err != nil || strings.ContainsAny(value, "\r\n\x00") {
			return ErrInvalidInput
		}
		addresses = append(addresses, address.Address)
	}
	input := SMTPTestInput{Server: runtime.SMTPServer, Port: runtime.SMTPPort, Username: runtime.SMTPUsername, Password: runtime.SMTPPassword}
	_, err = s.withSMTP(ctx, input, func(client *smtp.Client) error {
		if err := client.Mail(from.Address); err != nil {
			return err
		}
		for _, address := range addresses {
			if err := client.Rcpt(address); err != nil {
				return err
			}
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(body))
		var lines strings.Builder
		for len(encoded) > 76 {
			lines.WriteString(encoded[:76] + "\r\n")
			encoded = encoded[76:]
		}
		lines.WriteString(encoded + "\r\n")
		message := "From: " + from.String() + "\r\nTo: " + strings.Join(addresses, ", ") + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + lines.String()
		if _, err := writer.Write([]byte(message)); err != nil {
			_ = writer.Close()
			return err
		}
		return writer.Close()
	})
	return err
}

func (s *Service) withSMTP(ctx context.Context, input SMTPTestInput, deliver func(*smtp.Client) error) (SMTPTestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	runtime, err := s.Runtime()
	if err != nil {
		return SMTPTestResult{}, err
	}
	server := strings.TrimSpace(input.Server)
	username := strings.TrimSpace(input.Username)
	password := input.Password
	if input.UseStoredPassword && password == "" && server == runtime.SMTPServer && username == runtime.SMTPUsername {
		password = runtime.SMTPPassword
	}
	if server == "" || input.Port < 1 || input.Port > 65535 {
		return SMTPTestResult{}, ErrInvalidInput
	}
	if strings.ContainsAny(server+username, "\r\n\x00") {
		return SMTPTestResult{}, ErrInvalidInput
	}

	dialer := &net.Dialer{Timeout: 8 * time.Second}
	address := net.JoinHostPort(server, strconv.Itoa(input.Port))
	result := SMTPTestResult{Server: address, Stages: make([]SMTPTestStage, 0, 5)}
	addresses, err := net.DefaultResolver.LookupHost(ctx, server)
	if err != nil {
		return failedSMTPStage(result, "dns", err)
	}
	result.Stages = append(result.Stages, SMTPTestStage{Name: "dns", Status: "success", Detail: strings.Join(addresses, ", ")})

	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return failedSMTPStage(result, "connect", err)
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return failedSMTPStage(result, "connect", err)
		}
	}
	result.Stages = append(result.Stages, SMTPTestStage{Name: "connect", Status: "success", Detail: address})

	var client *smtp.Client
	if input.Port == 465 {
		tlsConnection := tls.Client(connection, &tls.Config{ServerName: server, MinVersion: tls.VersionTLS12})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return failedSMTPStage(result, "tls", err)
		}
		result.TLS = true
		client, err = smtp.NewClient(tlsConnection, server)
	} else {
		client, err = smtp.NewClient(connection, server)
	}
	if err != nil {
		return failedSMTPStage(result, "connect", err)
	}
	defer client.Close()

	if !result.TLS {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(&tls.Config{ServerName: server, MinVersion: tls.VersionTLS12}); err != nil {
				return failedSMTPStage(result, "tls", err)
			}
			result.TLS = true
		}
	}
	if result.TLS {
		result.Stages = append(result.Stages, SMTPTestStage{Name: "tls", Status: "success", Detail: "TLS 1.2 or newer"})
	} else {
		result.Stages = append(result.Stages, SMTPTestStage{Name: "tls", Status: "skipped", Detail: "server did not advertise STARTTLS"})
	}
	if username != "" {
		if password == "" {
			return failedSMTPStage(result, "auth", errors.New("SMTP password is required"))
		}
		if !result.TLS && !isLoopbackServer(server) {
			return failedSMTPStage(result, "auth", errors.New("SMTP server does not support TLS; credentials were not sent"))
		}
		if ok, _ := client.Extension("AUTH"); !ok {
			return failedSMTPStage(result, "auth", errors.New("SMTP server does not advertise authentication"))
		}
		if err := client.Auth(smtp.PlainAuth("", username, password, server)); err != nil {
			return failedSMTPStage(result, "auth", err)
		}
		result.Authenticated = true
		result.Stages = append(result.Stages, SMTPTestStage{Name: "auth", Status: "success", Detail: username})
	} else {
		result.Stages = append(result.Stages, SMTPTestStage{Name: "auth", Status: "skipped", Detail: "no username configured"})
	}
	if err := client.Noop(); err != nil {
		return failedSMTPStage(result, "verify", err)
	}
	if deliver != nil {
		if err := deliver(client); err != nil {
			return failedSMTPStage(result, "send", err)
		}
	}
	result.Stages = append(result.Stages, SMTPTestStage{Name: "verify", Status: "success", Detail: "SMTP NOOP accepted"})
	_ = client.Quit()
	return result, nil
}

func failedSMTPStage(result SMTPTestResult, name string, err error) (SMTPTestResult, error) {
	result.Stages = append(result.Stages, SMTPTestStage{Name: name, Status: "failed", Detail: err.Error()})
	return result, fmt.Errorf("%s SMTP stage: %w", name, err)
}

func isLoopbackServer(server string) bool {
	if strings.EqualFold(server, "localhost") {
		return true
	}
	ip := net.ParseIP(server)
	return ip != nil && ip.IsLoopback()
}
