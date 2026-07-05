// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package shared

import (
	"bufio"
	"bytes"
	"testing"
)

// buildMCHandshakePacket 은 테스트용 MC 핸드셰이크 패킷 바이트를 생성합니다.
func buildMCHandshakePacket(protocol int, addr string, port uint16, nextState int) []byte {
	var buf []byte

	// Packet ID = 0x00
	buf = appendVarInt(buf, 0x00)
	// Protocol Version
	buf = appendVarInt(buf, protocol)
	// Server Address
	buf = appendString(buf, addr)
	// Server Port
	buf = append(buf, byte(port>>8), byte(port))
	// Next State
	buf = appendVarInt(buf, nextState)

	// Packet Length prefix
	pkt := appendVarInt(nil, len(buf))
	pkt = append(pkt, buf...)
	return pkt
}

func appendVarInt(buf []byte, val int) []byte {
	for {
		b := byte(val & 0x7F)
		val >>= 7
		if val != 0 {
			b |= 0x80
		}
		buf = append(buf, b)
		if val == 0 {
			break
		}
	}
	return buf
}

func appendString(buf []byte, s string) []byte {
	buf = appendVarInt(buf, len(s))
	buf = append(buf, s...)
	return buf
}

func TestPeekMCHandshake_Valid(t *testing.T) {
	tests := []struct {
		name      string
		protocol  int
		addr      string
		port      uint16
		nextState int
	}{
		{"status 1.20.4", 765, "mc.example.com", 25565, 1},
		{"login 1.19.4", 762, "localhost", 25565, 2},
		{"status 1.7.2", 4, "192.168.1.1", 19132, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildMCHandshakePacket(tt.protocol, tt.addr, tt.port, tt.nextState)
			info, _, err := PeekMCHandshake(data)
			if err != nil {
				t.Fatalf("PeekMCHandshake: %v", err)
			}
			if info.ProtocolVersion != tt.protocol {
				t.Errorf("Protocol: got %d, want %d", info.ProtocolVersion, tt.protocol)
			}
			if info.ServerAddress != tt.addr {
				t.Errorf("Address: got %q, want %q", info.ServerAddress, tt.addr)
			}
			if info.NextState != tt.nextState {
				t.Errorf("NextState: got %d, want %d", info.NextState, tt.nextState)
			}
		})
	}
}

func TestPeekMCHandshake_Invalid(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"too short", []byte{0x01}},
		{"not handshake", []byte{0x03, 0x01, 0xFE, 0x01, 0x00}}, // PacketID != 0x00
		{"wrong next state", buildMCHandshakePacket(765, "mc.example.com", 25565, 3)},
		{"too small protocol", buildMCHandshakePacket(0, "mc.example.com", 25565, 1)},
		{"random bytes", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := PeekMCHandshake(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParseLoginFromPeekData(t *testing.T) {
	handshake := buildMCHandshakePacket(762, "localhost", 25565, 2)
	loginStart := buildLoginStartPacket("Steve", true)
	data := append(handshake, loginStart...)

	info, err := ParseLoginFromPeekData(data)
	if err != nil {
		t.Fatalf("ParseLoginFromPeekData: %v", err)
	}
	if info.PlayerName != "Steve" {
		t.Errorf("PlayerName: got %q, want %q", info.PlayerName, "Steve")
	}
	if info.ProtocolVersion != 762 {
		t.Errorf("ProtocolVersion: got %d, want %d", info.ProtocolVersion, 762)
	}
}

func TestParseLoginFromPeekData_NoUUID(t *testing.T) {
	handshake := buildMCHandshakePacket(760, "localhost", 25565, 2)
	loginStart := buildLoginStartPacket("Alex", false) // 1.19.1 — no UUID in packet
	data := append(handshake, loginStart...)

	info, err := ParseLoginFromPeekData(data)
	if err != nil {
		t.Fatalf("ParseLoginFromPeekData: %v", err)
	}
	if info.PlayerName != "Alex" {
		t.Errorf("PlayerName: got %q, want %q", info.PlayerName, "Alex")
	}
	if info.PlayerUUID != "" {
		t.Errorf("PlayerUUID: got %q, want empty", info.PlayerUUID)
	}
	if info.ProtocolVersion != 760 {
		t.Errorf("ProtocolVersion: got %d, want %d", info.ProtocolVersion, 760)
	}
}

func TestReadFullPacketFromBufio(t *testing.T) {
	msg := "test message"
	var body []byte
	body = appendVarInt(body, 0x04) // Packet ID
	body = appendString(body, msg)

	pkt := appendVarInt(nil, len(body)) // Packet Length
	pkt = append(pkt, body...)

	reader := bufio.NewReaderSize(bytes.NewReader(pkt), 1024)
	pktID, _, raw, err := ReadFullPacketFromBufio(reader)
	if err != nil {
		t.Fatalf("ReadFullPacketFromBufio: %v", err)
	}
	if pktID != 0x04 {
		t.Errorf("pktID: got 0x%02x, want 0x%02x", pktID, 0x04)
	}
	if !bytes.Equal(raw, pkt) {
		t.Errorf("raw mismatch:\ngot:  %x\nwant: %x", raw, pkt)
	}
}

func TestReadInitialPackets(t *testing.T) {
	handshake := buildMCHandshakePacket(762, "localhost", 25565, 2)
	loginStart := buildLoginStartPacket("Steve", true)
	allPkt := append(handshake, loginStart...)

	reader := bufio.NewReaderSize(bytes.NewReader(allPkt), 4096)
	raw, info, err := ReadInitialPackets(reader)
	if err != nil {
		t.Fatalf("ReadInitialPackets: %v", err)
	}
	if !bytes.Equal(raw, allPkt) {
		t.Errorf("raw mismatch:\ngot:  %x\nwant: %x", raw, allPkt)
	}
	if info.PlayerName != "Steve" {
		t.Errorf("PlayerName: got %q, want %q", info.PlayerName, "Steve")
	}
	if info.ProtocolVersion != 762 {
		t.Errorf("ProtocolVersion: got %d, want %d", info.ProtocolVersion, 762)
	}
}

// buildLoginStartPacket 은 테스트용 Login Start 패킷을 생성합니다.
func buildLoginStartPacket(name string, withUUID bool) []byte {
	var body []byte
	body = appendVarInt(body, 0x00) // Packet ID
	body = appendString(body, name)
	if withUUID {
		// 더미 UUID 16바이트
		uuid := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10}
		body = append(body, uuid...)
	}

	pkt := appendVarInt(nil, len(body))
	pkt = append(pkt, body...)
	return pkt
}
