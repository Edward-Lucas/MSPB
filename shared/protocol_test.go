// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package shared

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		f    Frame
	}{
		{"handshake", Frame{Type: MsgHandshake, Payload: []byte{0x01, 0x02, 0x03}}},
		{"heartbeat", Frame{Type: MsgHeartbeat, Payload: nil}},
		{"port_assign", Frame{Type: MsgPortAssign, Payload: []byte("hello")}},
		{"empty_payload", Frame{Type: MsgClose, Payload: []byte{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tt.f); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			got, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if got.Type != tt.f.Type {
				t.Errorf("Type: got 0x%02x, want 0x%02x", got.Type, tt.f.Type)
			}
			if !bytes.Equal(got.Payload, tt.f.Payload) {
				t.Errorf("Payload: got %v, want %v", got.Payload, tt.f.Payload)
			}
		})
	}
}

func TestHandshakeRequestRoundTrip(t *testing.T) {
	tests := []struct {
		token string
		port  uint16
	}{
		{"", 25565},
		{"my-secret-token", 19132},
		{"abc", 1},
	}

	for _, tt := range tests {
		req := &HandshakeRequest{Token: tt.token, ServerPort: tt.port}
		data := req.Marshal()
		got, err := UnmarshalHandshakeRequest(data)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.Token != tt.token {
			t.Errorf("Token: got %q, want %q", got.Token, tt.token)
		}
		if got.ServerPort != tt.port {
			t.Errorf("ServerPort: got %d, want %d", got.ServerPort, tt.port)
		}
	}
}

func TestPortAssignmentRoundTrip(t *testing.T) {
	tests := []struct {
		port     uint16
		success  bool
		serverIP string
		message  string
	}{
		{25500, true, "203.0.113.1", "success"},
		{0, false, "", "no ports available"},
		{25600, true, "10.0.0.1", ""},
	}

	for _, tt := range tests {
		pa := &PortAssignment{AssignedPort: tt.port, Success: tt.success, ServerIP: tt.serverIP, Message: tt.message}
		data := pa.Marshal()
		got, err := UnmarshalPortAssignment(data)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.AssignedPort != tt.port {
			t.Errorf("Port: got %d, want %d", got.AssignedPort, tt.port)
		}
		if got.Success != tt.success {
			t.Errorf("Success: got %v, want %v", got.Success, tt.success)
		}
		if got.ServerIP != tt.serverIP {
			t.Errorf("ServerIP: got %q, want %q", got.ServerIP, tt.serverIP)
		}
		if got.Message != tt.message {
			t.Errorf("Message: got %q, want %q", got.Message, tt.message)
		}
	}
}
