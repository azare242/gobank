package socketserver

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"unicode"
	"unicode/utf8"
)

const RequestLimit = 16 * 1024
const ResponseLimit = 1024 * 1024

type Request struct {
	Version   int             `json:"version"`
	RequestID string          `json:"request_id,omitempty"`
	Operation string          `json:"operation"`
	Token     string          `json:"token,omitempty"`
	Username  string          `json:"username,omitempty"`
	Password  string          `json:"password,omitempty"`
	ID        uint            `json:"id,omitempty"`
	Account   string          `json:"account,omitempty"`
	Amount    json.RawMessage `json:"amount,omitempty"`
}
type Response struct {
	Version    int    `json:"version"`
	RequestID  string `json:"request_id"`
	Status     string `json:"status"`
	StatusCode int    `json:"status_code"`
	Data       any    `json:"data,omitempty"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
}

func ReadFrame(r io.Reader, limit uint32) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > limit {
		return nil, errors.New("invalid frame length")
	}
	data := make([]byte, n)
	_, err := io.ReadFull(r, data)
	return data, err
}
func WriteFrame(w io.Writer, data []byte, limit uint32) error {
	if len(data) == 0 || uint64(len(data)) > uint64(limit) {
		return errors.New("invalid response length")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	for _, part := range [][]byte{header[:], data} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 || n > len(part) {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
func decode(data []byte) (Request, error) {
	var request Request
	if !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return request, errors.New("invalid JSON object")
	}
	// Reject null scalars, which encoding/json otherwise treats as zero values.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return request, err
	}
	// Keep safe audit intent even when another envelope field has an invalid type.
	_ = json.Unmarshal(fields["operation"], &request.Operation)
	_ = json.Unmarshal(fields["account"], &request.Account)
	_ = json.Unmarshal(fields["username"], &request.Username)
	for name, v := range fields {
		if name != "amount" && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return request, errors.New("null field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if request.Version != 1 || request.Operation == "" {
		return request, errors.New("version 1 and operation required")
	}
	if len(request.RequestID) > 128 {
		return request, errors.New("request ID too long")
	}
	for _, c := range request.RequestID {
		if unicode.IsControl(c) {
			return request, errors.New("invalid request ID")
		}
	}
	return request, nil
}
