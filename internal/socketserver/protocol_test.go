package socketserver

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

func TestFragmentedFrames(t *testing.T) {
	reader, writer := net.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		defer writer.Close()
		var stream bytes.Buffer
		for _, payload := range []string{"first", "second"} {
			WriteFrame(&stream, []byte(payload), RequestLimit)
		}
		for _, b := range stream.Bytes() {
			if _, err := writer.Write([]byte{b}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for _, want := range []string{"first", "second"} {
		data, err := ReadFrame(reader, RequestLimit)
		if err != nil || string(data) != want {
			t.Fatal(string(data), err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestFrames(t *testing.T) {
	w := &shortWriter{}
	for _, p := range []string{"first", "second"} {
		if err := WriteFrame(w, []byte(p), RequestLimit); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"first", "second"} {
		p, err := ReadFrame(w, RequestLimit)
		if err != nil || string(p) != want {
			t.Fatal(string(p), err)
		}
	}
	for _, n := range []uint32{0, RequestLimit + 1} {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], n)
		if _, err := ReadFrame(bytes.NewReader(b[:]), RequestLimit); err == nil {
			t.Fatal("bad length accepted")
		}
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 3, 1}), RequestLimit); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	for _, p := range []string{`null`, `[]`, `{"version":2,"operation":"health"}`, `{"version":1,"operation":null}`, `{"version":1,"operation":"health","extra":1}`, `{"version":1,"operation":"health"} {}`} {
		if _, err := decode([]byte(p)); err == nil {
			t.Fatal(p)
		}
	}
	if _, err := decode([]byte(`{"version":1,"operation":"health"}`)); err != nil {
		t.Fatal(err)
	}
}
