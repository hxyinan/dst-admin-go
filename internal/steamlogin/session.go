package steamlogin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type loginError struct{ code, message string }

func (e *loginError) Error() string { return e.message }
func errorCode(err error) string {
	var login *loginError
	if errors.As(err, &login) {
		return login.code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "LOGIN_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "LOGIN_CANCELLED"
	}
	return "LOGIN_FAILED"
}
func authError(code string) error {
	messages := map[string]string{
		"GUARD_REQUIRED":   "该节点的 Steam 账号需要验证，请打开 Steam 登录设置完成验证后重试下载",
		"INVALID_PASSWORD": "Steam 账号或密码错误，请检查登录设置",
		"ACCESS_DENIED":    "Steam 拒绝访问该内容，请检查账号权限和应用 ID",
		"STEAM_NETWORK":    "无法连接 Steam，请检查运行节点的网络后重试",
		"LOGIN_FAILED":     "Steam 登录失败，请在登录设置中重新验证账号",
	}
	return &loginError{code: code, message: messages[code]}
}

type loginSession struct {
	id             string
	command        *exec.Cmd
	stdin          io.WriteCloser
	cancel         context.CancelFunc
	done           chan struct{}
	wake           chan struct{}
	mu             sync.Mutex
	inputMu        sync.Mutex
	pending, state string
	secrets        []string
	output         io.Writer
	waitErr        error
	closed         bool
	discardLine    bool
	downloadErr    error
}

func startSession(ctx context.Context, executable string, value credentials, output io.Writer, preparation ...string) (*loginSession, error) {
	if output == nil {
		output = io.Discard
	}
	s := &loginSession{id: uuid.NewString(), done: make(chan struct{}), wake: make(chan struct{}, 1), output: output, secrets: []string{value.Password}, state: "connecting"}
	s.command = exec.CommandContext(ctx, executable)
	configureProcess(s.command)
	s.command.Stdout, s.command.Stderr = s, s
	stdin, err := s.command.StdinPipe()
	if err != nil {
		return nil, err
	}
	s.stdin = stdin
	if err = s.command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	go func() {
		err := s.command.Wait()
		s.mu.Lock()
		s.waitErr = err
		s.closed = true
		s.flushLocked(true)
		s.mu.Unlock()
		close(s.done)
	}()
	prefix := ""
	if len(preparation) > 0 {
		prefix = strings.Join(preparation, "\n") + "\n"
	}
	if err = s.send(prefix + "login " + quote(value.Username) + " " + quote(value.Password) + "\n"); err != nil {
		s.stop()
		return nil, err
	}
	return s, nil
}

func (s *loginSession) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending += string(data)
	s.flushLocked(false)
	// Steam Guard prompts have no newline. Parse the bounded current line, not
	// historical output, so an old prompt cannot override later successful login.
	if !s.discardLine {
		state := classify(s.protocolLine(s.pending, true))
		if (state == "ready" || strings.HasPrefix(state, "guard_")) && (s.state == "connecting" || strings.HasPrefix(s.state, "guard_")) {
			s.state = state
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return len(data), nil
}

// Buffer across fragmented writes: a password split between stdout chunks must
// never escape redaction. Raw text is kept in memory only for prompt detection.
func (s *loginSession) flushLocked(final bool) {
	for {
		index := strings.IndexAny(s.pending, "\r\n")
		if s.discardLine {
			if index < 0 {
				s.pending = ""
				return
			}
			s.pending = s.pending[index+1:]
			s.discardLine = false
			continue
		}
		if index < 0 {
			if len(s.pending) > 65536 {
				// Never emit part of a long line: a secret could straddle the
				// truncation boundary. Drop it through the next line break.
				s.pending = ""
				s.discardLine = !final
				_, _ = io.WriteString(s.output, "[SteamCMD overlong output line hidden]\n")
				return
			}
			if !final {
				return
			}
			if s.pending == "" {
				return
			}
			index = len(s.pending)
		}
		line := s.pending[:index]
		s.pending = s.pending[index:]
		if len(s.pending) > 0 {
			s.pending = s.pending[1:]
		}
		if len(line) > 65536 {
			line = "[SteamCMD overlong output line hidden]"
		}
		s.parseLocked(line)
		line = s.redact(line)
		_, _ = io.WriteString(s.output, line+"\n")
	}
}
func (s *loginSession) redact(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	if strings.HasPrefix(lower, "login ") || strings.Contains(lower, "steam>login ") || strings.Contains(lower, "steam> login ") {
		return "[Steam login command hidden]"
	}
	for _, secret := range s.secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, quote(secret), "[redacted]")
			line = strings.ReplaceAll(line, secret, "[redacted]")
		}
	}
	return line
}

func (s *loginSession) protocolLine(line string, incomplete bool) string {
	value := strings.TrimSpace(line)
	// Command echoes and a bare echoed credential are not protocol messages.
	// Do not redact substrings before parsing: passwords such as "OK" must not
	// hide the real "Waiting for user info...OK" readiness message.
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "login ") || strings.HasPrefix(lower, "steam>login ") || strings.HasPrefix(lower, "steam> login ") {
		return ""
	}
	for _, secret := range s.secrets {
		if secret != "" && (value == secret || value == quote(secret) || (incomplete && (strings.HasPrefix(secret, value) || strings.HasPrefix(quote(secret), value)))) {
			return ""
		}
	}
	return line
}
func (s *loginSession) parseLocked(line string) {
	protocol := s.protocolLine(line, false)
	state := classify(protocol)
	value := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(protocol)), "steam>"))
	// Keep the first explicit download failure even if later commands succeed
	// or SteamCMD exits zero. Only structured failure lines count as failures.
	if s.downloadErr == nil {
		if state == "ACCESS_DENIED" || state == "STEAM_NETWORK" || state == "INVALID_PASSWORD" || state == "LOGIN_FAILED" {
			s.downloadErr = authError(state)
		} else if strings.HasPrefix(value, "error!") || strings.HasPrefix(value, "update canceled:") || strings.HasPrefix(value, "update cancelled:") {
			s.downloadErr = fmt.Errorf("SteamCMD 下载或校验失败: %s", strings.TrimSpace(s.redact(line)))
		}
	}
	if state != "" && (s.state == "" || s.state == "connecting" || strings.HasPrefix(s.state, "guard_") || s.state == "ready") {
		s.state = state
	}
}
func classify(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.TrimSpace(strings.TrimPrefix(value, "steam>"))
	if strings.HasPrefix(value, "logging in user ") {
		// Extract only the result after the quoted username and service name.
		index := strings.Index(value, "' to steam ")
		if index < 0 {
			return ""
		}
		suffix := value[index+len("' to steam "):]
		index = strings.Index(suffix, "...")
		if index < 0 {
			return ""
		}
		value = strings.TrimSpace(suffix[index+3:])
	}
	if strings.HasPrefix(value, "login ") {
		return ""
	}
	failure := strings.HasPrefix(value, "error!") || strings.HasPrefix(value, "error:") || strings.HasPrefix(value, "failed (") || strings.HasPrefix(value, "failed to login") || strings.HasPrefix(value, "failed to logon") || strings.HasPrefix(value, "failed to connect")
	if failure {
		if strings.Contains(value, "invalidpassword") || strings.Contains(value, "invalid password") {
			return "INVALID_PASSWORD"
		}
		if strings.Contains(value, "nosubscription") || strings.Contains(value, "no subscription") || strings.Contains(value, "access denied") {
			return "ACCESS_DENIED"
		}
		if strings.Contains(value, "noconnection") || strings.Contains(value, "no connection") || strings.Contains(value, "failed to connect") || strings.Contains(value, "timeout") {
			return "STEAM_NETWORK"
		}
		if strings.Contains(value, "failed to login") || strings.Contains(value, "failed to logon") {
			return "LOGIN_FAILED"
		}
		return ""
	}
	prompt := ""
	if strings.HasPrefix(value, "waiting for confirmation") || strings.HasPrefix(value, "please confirm the login") {
		prompt = "guard_approval"
	} else if strings.HasPrefix(value, "steam guard code:") {
		prompt = "guard_email"
	} else if strings.HasPrefix(value, "two-factor code:") || strings.HasPrefix(value, "two factor code:") || strings.HasPrefix(value, "twofactor code:") {
		prompt = "guard_mobile"
	} else if (strings.HasPrefix(value, "please enter ") || strings.HasPrefix(value, "enter ")) && (strings.Contains(value, "code") || strings.Contains(value, "twofactor")) {
		if strings.Contains(value, "authenticator") || strings.Contains(value, "two-factor") || strings.Contains(value, "two factor") || strings.Contains(value, "twofactor") {
			prompt = "guard_mobile"
		} else if strings.Contains(value, "steam guard") || strings.Contains(value, "email") {
			prompt = "guard_email"
		}
	}
	// Approval and readiness can be printed consecutively without a newline.
	if prompt != "" {
		if index := strings.LastIndex(value, ":"); index >= 0 {
			if ready := classify(value[index+1:]); ready == "ready" {
				return ready
			}
		}
		return prompt
	}
	if strings.HasPrefix(value, "waiting for user info...ok") || strings.HasPrefix(value, "waiting for user info ...ok") || strings.HasPrefix(value, "logged in ok") {
		return "ready"
	}
	return ""
}
func (s *loginSession) authenticate(ctx context.Context, notify func(string), interactive bool) error {
	for {
		s.mu.Lock()
		state := s.state
		closed := s.closed
		s.mu.Unlock()
		if state == "ready" {
			return nil
		}
		if state != "connecting" && state != "guard_email" && state != "guard_mobile" && state != "guard_approval" {
			return authError(state)
		}
		if state == "guard_email" || state == "guard_mobile" || state == "guard_approval" {
			if !interactive {
				return authError("GUARD_REQUIRED")
			}
			if notify != nil {
				notify(state)
			}
		}
		if closed {
			return authError("LOGIN_FAILED")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
		case <-s.done:
		}
	}
}
func (s *loginSession) send(value string) error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	_, err := io.WriteString(s.stdin, value)
	return err
}
func (s *loginSession) sendCode(code string) error {
	code = strings.ToUpper(code)
	s.mu.Lock()
	s.secrets = append(s.secrets, code)
	s.flushLocked(true)
	s.downloadErr = nil
	s.state = "connecting"
	s.mu.Unlock()
	return s.send(code + "\n")
}
func (s *loginSession) stop() {
	s.mu.Lock()
	ready, closed := s.state == "ready", s.closed
	s.mu.Unlock()
	if ready && !closed {
		_ = s.send("quit\n")
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	}
	_ = s.stdin.Close()
	select {
	case <-s.done:
	default:
		if s.command.Cancel != nil {
			_ = s.command.Cancel()
		}
	}
	<-s.done
	if s.cancel != nil {
		s.cancel()
	}
}
func (s *loginSession) result() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.downloadErr != nil {
		return s.downloadErr
	}
	if s.waitErr != nil {
		return fmt.Errorf("SteamCMD 执行失败: %w", s.waitErr)
	}
	return nil
}
