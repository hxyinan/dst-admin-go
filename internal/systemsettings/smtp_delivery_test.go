package systemsettings

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestSMTPReallySendsUTF8MessageAndRejectsRecipients(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			captured := make(chan string, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				fmt.Fprint(conn, "220 localhost ready\r\n")
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					command := scanner.Text()
					switch {
					case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"), command == "NOOP", strings.HasPrefix(command, "MAIL FROM"):
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(command, "RCPT TO"):
						if reject {
							fmt.Fprint(conn, "550 recipient refused\r\n")
						} else {
							fmt.Fprint(conn, "250 OK\r\n")
						}
					case command == "DATA":
						fmt.Fprint(conn, "354 send data\r\n")
						var content strings.Builder
						for scanner.Scan() {
							if scanner.Text() == "." {
								break
							}
							content.WriteString(scanner.Text() + "\r\n")
						}
						captured <- content.String()
						fmt.Fprint(conn, "250 accepted\r\n")
					case command == "QUIT":
						fmt.Fprint(conn, "221 goodbye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unexpected command\r\n")
					}
				}
			}()
			service, _ := NewService(NewMemoryRepository())
			settings, _ := service.Settings()
			_, err = service.Apply(Input{Revision: settings.Revision, Confirmation: ApplyConfirmation, Values: map[string]string{
				"notification.smtpServer": "127.0.0.1", "notification.smtpPort": fmt.Sprint(listener.Addr().(*net.TCPAddr).Port), "notification.senderEmail": "sender@example.com", "notification.smtpUsername": "",
			}})
			if err != nil {
				t.Fatal(err)
			}
			err = service.SendMail(context.Background(), []string{"user@example.com"}, "饥荒备份 / Backup", "已完成\n第二行")
			if reject {
				if err == nil {
					t.Fatal("SMTP rejection ignored")
				}
				<-done
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			<-done
			message, err := mail.ReadMessage(strings.NewReader(<-captured))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "饥荒备份 / Backup" {
				t.Fatal("incorrect UTF-8 subject", subject, err)
			}
			body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
			if err != nil || string(body) != "已完成\n第二行" {
				t.Fatal("incorrect UTF-8 body", string(body), err)
			}
			if message.Header.Get("To") != "user@example.com" || message.Header.Get("From") != "<sender@example.com>" {
				t.Fatal("incorrect envelope headers", message.Header)
			}
		})
	}
}

func TestSMTPCancellationClosesStalledConnectionPromptly(t *testing.T) {
	server, port := startSMTPTestServer(t, smtpTestStall)
	service, _ := NewService(NewMemoryRepository())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(25 * time.Millisecond); cancel() }()
	started := time.Now()
	if _, err := service.TestSMTP(ctx, SMTPTestInput{Server: server, Port: port}); err == nil {
		t.Fatal("cancel ignored")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("cancellation did not close socket promptly")
	}
}
