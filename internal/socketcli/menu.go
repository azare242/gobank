// Package socketcli keeps terminal interaction separate from the network client.
package socketcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/azare242/gobank/internal/socketclient"
	"github.com/azare242/gobank/internal/socketserver"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Transport interface {
	Call(context.Context, socketserver.Request) (socketclient.Response, error)
	Close() error
}

var errInputTooLong = errors.New("input exceeds 1024 bytes")

type Menu struct {
	In           io.Reader
	Out          io.Writer
	Client       Transport
	ReadPassword func() ([]byte, error)
	token        string
	profile      struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Account  string `json:"account"`
	}
}

// line reads one byte at a time so terminal password reads never compete with buffered read-ahead.
func (m *Menu) line(prompt string) (string, error) {
	fmt.Fprint(m.Out, prompt)
	var data []byte
	var b [1]byte
	overflow := false
	for {
		n, err := m.In.Read(b[:])
		if n > 0 {
			if b[0] == '\n' {
				if overflow {
					return "", errInputTooLong
				}
				return strings.TrimSuffix(string(data), "\r"), nil
			}
			if len(data) < 1024 {
				data = append(data, b[0])
			} else {
				overflow = true
			}
		}
		if err != nil {
			if err == io.EOF && len(data) > 0 && !overflow {
				return strings.TrimSuffix(string(data), "\r"), nil
			}
			return "", err
		}
	}
}
func (m *Menu) clear() {
	m.token = ""
	m.profile.ID = 0
	m.profile.Username = ""
	m.profile.Account = ""
}
func (m *Menu) call(ctx context.Context, r socketserver.Request) (socketclient.Response, bool) {
	r.Token = m.token
	response, err := m.Client.Call(ctx, r)
	if err != nil {
		fmt.Fprintf(m.Out, "Connection or protocol error: %v. The next selected operation will reconnect; no request was replayed.\n", err)
		return socketclient.Response{}, false
	}
	if response.Status == "error" {
		fmt.Fprintf(m.Out, "%s: %s\n", response.Error, response.Message)
		if response.Error == "unauthorized" {
			m.clear()
			fmt.Fprintln(m.Out, "Please log in again.")
		}
		return response, false
	}
	return response, true
}
func (m *Menu) credentials() (socketserver.Request, error) {
	var r socketserver.Request
	for {
		username, err := m.line("Username: ")
		if err != nil {
			if errors.Is(err, errInputTooLong) {
				fmt.Fprintln(m.Out, err)
				continue
			}
			return r, err
		}
		username = strings.TrimSpace(username)
		if n := utf8.RuneCountInString(username); n < 1 || n > 64 {
			fmt.Fprintln(m.Out, "Use 1 to 64 characters.")
			continue
		}
		r.Username = username
		break
	}
	for {
		var password []byte
		var err error
		if m.ReadPassword != nil {
			fmt.Fprint(m.Out, "Password: ")
			password, err = m.ReadPassword()
			fmt.Fprintln(m.Out)
		} else {
			var text string
			text, err = m.line("Password: ")
			password = []byte(text)
			text = ""
		}
		if err != nil {
			clear(password)
			if errors.Is(err, errInputTooLong) {
				fmt.Fprintln(m.Out, err)
				continue
			}
			return r, err
		}
		if len(password) < 8 || len(password) > 72 {
			clear(password)
			fmt.Fprintln(m.Out, "Use 8 to 72 password bytes.")
			continue
		}
		r.Password = string(password)
		clear(password)
		return r, nil
	}
}
func (m *Menu) showProfile(ctx context.Context) bool {
	response, ok := m.call(ctx, socketserver.Request{Operation: "me"})
	if !ok {
		return false
	}
	if err := json.Unmarshal(response.Data, &m.profile); err != nil || m.profile.ID == 0 || m.profile.Username == "" || m.profile.Account == "" {
		fmt.Fprintln(m.Out, "Invalid profile response.")
		return false
	}
	fmt.Fprintf(m.Out, "User: %s (ID %d), account: %s\n", m.profile.Username, m.profile.ID, m.profile.Account)
	return true
}
func (m *Menu) balance(ctx context.Context) {
	response, ok := m.call(ctx, socketserver.Request{Operation: "balance", Account: m.profile.Account})
	if !ok {
		return
	}
	m.showBalance(response.Data)
}
func (m *Menu) showBalance(data json.RawMessage) {
	var a struct {
		Account string `json:"account"`
		Balance int64  `json:"balance"`
	}
	if json.Unmarshal(data, &a) != nil || a.Account == "" {
		fmt.Fprintln(m.Out, "Invalid balance response.")
		return
	}
	fmt.Fprintf(m.Out, "Account %s balance: %d\n", a.Account, a.Balance)
}
func (m *Menu) change(ctx context.Context, op string) error {
	var amount int64
	for {
		text, err := m.line("Amount: ")
		if err != nil {
			return err
		}
		amount, err = strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil || amount <= 0 {
			fmt.Fprintln(m.Out, "Enter a positive int64 integer.")
			continue
		}
		break
	}
	for {
		confirm, err := m.line(fmt.Sprintf("Confirm %s of %d on %s? [y/n]: ", op, amount, m.profile.Account))
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(confirm)) {
		case "n", "no":
			fmt.Fprintln(m.Out, "Cancelled.")
			return nil
		case "y", "yes":
			response, ok := m.call(ctx, socketserver.Request{Operation: op, Account: m.profile.Account, Amount: json.RawMessage(strconv.FormatInt(amount, 10))})
			if ok {
				fmt.Fprintln(m.Out, "Change completed.")
				m.showBalance(response.Data)
			} else if response.Status == "" {
				fmt.Fprintln(m.Out, "The change may have committed. Do not retry it blindly.")
				answer, err := m.line("Check the current balance after reconnecting? [y/n]: ")
				if err != nil {
					return err
				}
				if strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes") {
					m.balance(ctx)
				}
			}
			return nil
		default:
			fmt.Fprintln(m.Out, "Enter y or n.")
		}
	}
}
func (m *Menu) logout(ctx context.Context) {
	if m.token == "" {
		return
	}
	_, ok := m.call(ctx, socketserver.Request{Operation: "logout"})
	m.clear()
	if ok {
		fmt.Fprintln(m.Out, "Logged out.")
	} else {
		fmt.Fprintln(m.Out, "Local session cleared; if logout could not reach the server, the session remains until expiration or revocation.")
	}
}
func (m *Menu) Run(ctx context.Context) (err error) {
	if m.In == nil || m.Out == nil || m.Client == nil {
		return errors.New("input, output and client are required")
	}
	defer func() {
		exitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.logout(exitCtx)
		m.Client.Close()
		fmt.Fprintln(m.Out, "Goodbye.")
	}()
	for {
		if ctx.Err() != nil {
			return nil
		}
		authenticated := m.token != ""
		if authenticated {
			fmt.Fprintf(m.Out, "\nSigned in as %s, account %s\n1. My profile\n2. Account balance\n3. Deposit\n4. Withdraw\n5. Logout\n6. Exit\n", m.profile.Username, m.profile.Account)
		} else {
			fmt.Fprintln(m.Out, "\n1. Register\n2. Login\n3. Health check\n4. Exit")
		}
		choice, readErr := m.line("Choice: ")
		if readErr != nil {
			if readErr == io.EOF || ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(m.Out, readErr)
			if errors.Is(readErr, errInputTooLong) {
				continue
			}
			return readErr
		}
		choice = strings.TrimSpace(choice)
		if !authenticated {
			switch choice {
			case "1", "2":
				request, err := m.credentials()
				if err != nil {
					if err == io.EOF || ctx.Err() != nil {
						return nil
					}
					return err
				}
				if choice == "1" {
					request.Operation = "create_user"
				} else {
					request.Operation = "login"
				}
				response, ok := m.call(ctx, request)
				request.Password = ""
				if !ok {
					continue
				}
				if choice == "1" {
					var profile struct {
						Username string `json:"username"`
						Account  string `json:"account"`
					}
					if json.Unmarshal(response.Data, &profile) != nil || profile.Account == "" {
						fmt.Fprintln(m.Out, "Invalid registration response.")
					} else {
						fmt.Fprintf(m.Out, "Registered %s, account %s. Log in to continue.\n", profile.Username, profile.Account)
					}
				} else {
					var login struct {
						Token   string `json:"session_token"`
						Expires int64  `json:"expires_in"`
					}
					if json.Unmarshal(response.Data, &login) != nil || len(login.Token) != 43 || login.Expires <= 0 {
						fmt.Fprintln(m.Out, "Invalid login response.")
						continue
					}
					m.token = login.Token
					login.Token = ""
					m.showProfile(ctx)
				}
			case "3":
				if _, ok := m.call(ctx, socketserver.Request{Operation: "health"}); ok {
					fmt.Fprintln(m.Out, "Server is healthy.")
				}
			case "4":
				return nil
			default:
				fmt.Fprintln(m.Out, "Invalid choice.")
			}
		} else {
			switch choice {
			case "1":
				m.showProfile(ctx)
			case "2":
				if m.profile.Account == "" {
					m.showProfile(ctx)
				}
				if m.token != "" && m.profile.Account != "" {
					m.balance(ctx)
				}
			case "3", "4":
				if m.profile.Account == "" && !m.showProfile(ctx) {
					continue
				}
				op := "deposit"
				if choice == "4" {
					op = "withdraw"
				}
				if err := m.change(ctx, op); err != nil {
					if err == io.EOF || ctx.Err() != nil {
						return nil
					}
					return err
				}
			case "5":
				m.logout(ctx)
			case "6":
				return nil
			default:
				fmt.Fprintln(m.Out, "Invalid choice.")
			}
		}
	}
}
