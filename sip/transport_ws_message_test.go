package sip

import (
	"bytes"
	"net"
	"testing"

	"github.com/gobwas/ws"
)

func TestWSConnectionReadsTextBinaryAndFragmentedMessages(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		frames []ws.Frame
	}{
		{
			name: "text",
			want: "OPTIONS sip:text.example SIP/2.0\r\n\r\n",
			frames: []ws.Frame{
				ws.MaskFrameInPlace(ws.NewTextFrame([]byte("OPTIONS sip:text.example SIP/2.0\r\n\r\n"))),
			},
		},
		{
			name: "binary",
			want: "OPTIONS sip:binary.example SIP/2.0\r\n\r\n",
			frames: []ws.Frame{
				ws.MaskFrameInPlace(ws.NewBinaryFrame([]byte("OPTIONS sip:binary.example SIP/2.0\r\n\r\n"))),
			},
		},
		{
			name: "fragmented binary",
			want: "OPTIONS sip:fragmented.example SIP/2.0\r\n\r\n",
			frames: []ws.Frame{
				ws.MaskFrameInPlace(ws.NewFrame(ws.OpBinary, false, []byte("OPTIONS sip:fragmented.example "))),
				ws.MaskFrameInPlace(ws.NewFrame(ws.OpContinuation, true, []byte("SIP/2.0\r\n\r\n"))),
			},
		},
		{
			name: "binary above generic stream buffer",
			want: string(bytes.Repeat([]byte{'a'}, int(TransportBufferReadSize)+1)),
			frames: []ws.Frame{
				ws.MaskFrameInPlace(ws.NewBinaryFrame(bytes.Repeat([]byte{'a'}, int(TransportBufferReadSize)+1))),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			writeDone := make(chan error, 1)
			go func() {
				for _, frame := range test.frames {
					if err := ws.WriteFrame(remote, frame); err != nil {
						writeDone <- err
						return
					}
				}
				writeDone <- nil
			}()
			connection := &WSConnection{Conn: local}
			buffer := make([]byte, ParseMaxMessageLength)
			n, err := connection.Read(buffer)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-writeDone; err != nil {
				t.Fatal(err)
			}
			if string(buffer[:n]) != test.want {
				t.Fatalf("message = %q, want %q", buffer[:n], test.want)
			}
		})
	}
}

func TestWSConnectionAnswersPingBeforeBinaryMessage(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	peerDone := make(chan error, 1)
	go func() {
		if err := ws.WriteFrame(remote, ws.MaskFrameInPlace(ws.NewPingFrame([]byte("keepalive")))); err != nil {
			peerDone <- err
			return
		}
		pong, err := ws.ReadFrame(remote)
		if err != nil {
			peerDone <- err
			return
		}
		if pong.Header.OpCode != ws.OpPong || string(pong.Payload) != "keepalive" {
			peerDone <- net.InvalidAddrError("invalid WebSocket pong")
			return
		}
		peerDone <- ws.WriteFrame(remote, ws.MaskFrameInPlace(ws.NewBinaryFrame([]byte("OPTIONS sip:ping.example SIP/2.0\r\n\r\n"))))
	}()
	connection := &WSConnection{Conn: local}
	buffer := make([]byte, ParseMaxMessageLength)
	n, err := connection.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "OPTIONS sip:ping.example SIP/2.0\r\n\r\n" {
		t.Fatalf("message after ping = %q", buffer[:n])
	}
}
