// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package shared

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ──────────────────────────────────────────────
// Minecraft Handshake Packet Peek
// ──────────────────────────────────────────────
//
// Minecraft Java Edition 프로토콜 핸드셰이크 패킷 구조:
//   [VarInt: Packet Length]
//   [VarInt: Packet ID]        (0x00 = Handshake)
//   [VarInt: Protocol Version]
//   [String: Server Address]
//   [Unsigned Short: Server Port]
//   [VarInt: Next State]       (1=Status, 2=Login)

// MCHandshakeInfo 는 파싱된 핸드셰이크 정보입니다.
type MCHandshakeInfo struct {
	ProtocolVersion int
	ServerAddress   string
	ServerPort      uint16
	NextState       int // 1=Status, 2=Login
}

// ReadVarInt 는 reader 로부터 VarInt를 읽습니다 (최대 5바이트).
func ReadVarInt(r io.ByteReader) (int, error) {
	var (
		result int
		shift  uint
	)
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= int(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
	}
	return 0, errors.New("varint too long")
}

// ReadString 는 MC 프로토콜의 String (VarInt length + UTF-8 bytes)을 읽습니다.
func ReadString(r io.ByteReader, maxLen int) (string, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return "", err
	}
	if length < 0 || length > maxLen {
		return "", errors.New("string length out of range")
	}
	buf := make([]byte, length)
	for i := 0; i < length; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		buf[i] = b
	}
	return string(buf), nil
}

// ReadUShort 는 Unsigned Short (2 bytes, big-endian)을 읽습니다.
func ReadUShort(r io.ByteReader) (uint16, error) {
	b1, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	b2, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16([]byte{b1, b2}), nil
}

// PeekMCHandshake 는 peek 데이터를 읽어 Minecraft 핸드셰이크인지 검증합니다.
// data 는 연결에서 peek 한 첫 바이트들이어야 합니다.
func PeekMCHandshake(data []byte) (*MCHandshakeInfo, int, error) {
	// 최소: VarInt(1) + VarInt(1) + VarInt(1) + String(1+1) + UShort(2) + VarInt(1) = ~7 bytes
	if len(data) < 7 {
		return nil, 0, errors.New("data too short for MC handshake")
	}

	r := &byteReader{data: data, pos: 0}

	// Packet Length (VarInt)
	pktLen, err := ReadVarInt(r)
	if err != nil {
		return nil, r.pos, err
	}
	if pktLen < 1 || pktLen > 1024 {
		return nil, r.pos, errors.New("invalid packet length")
	}

	// Packet ID (VarInt) — must be 0x00
	pktID, err := ReadVarInt(r)
	if err != nil {
		return nil, r.pos, err
	}
	if pktID != 0x00 {
		return nil, r.pos, errors.New("not a handshake packet")
	}

	// Protocol Version (VarInt)
	protoVer, err := ReadVarInt(r)
	if err != nil {
		return nil, r.pos, err
	}
	if protoVer < 4 { // 1.7.2 = protocol 4
		return nil, r.pos, errors.New("unsupported protocol version")
	}

	// Server Address (String, max 255)
	addr, err := ReadString(r, 255)
	if err != nil {
		return nil, r.pos, err
	}

	// Server Port (UShort)
	port, err := ReadUShort(r)
	if err != nil {
		return nil, r.pos, err
	}
	_ = port

	// Next State (VarInt) — 1=Status, 2=Login
	nextState, err := ReadVarInt(r)
	if err != nil {
		return nil, r.pos, err
	}
	if nextState != 1 && nextState != 2 {
		return nil, r.pos, errors.New("invalid next state")
	}

	return &MCHandshakeInfo{
		ProtocolVersion: protoVer,
		ServerAddress:   addr,
		ServerPort:      port,
		NextState:       nextState,
	}, r.pos, nil
}

// byteReader 는 []byte 슬라이스를 io.ByteReader 로 감싸는 헬퍼입니다.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) ReadByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

// ReadBytes 는 n 바이트를 읽습니다.
func (r *byteReader) ReadBytes(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, io.EOF
	}
	buf := make([]byte, n)
	copy(buf, r.data[r.pos:r.pos+n])
	r.pos += n
	return buf, nil
}

// ──────────────────────────────────────────────
// Minecraft Login Start Packet Parsing
// ──────────────────────────────────────────────
//
// Login Start 패킷 구조 (프로토콜 버전에 따라 다름):
//   [VarInt: Packet Length]
//   [VarInt: Packet ID]        (0x00 = Login Start)
//   [String: Player Name]      (최대 16자)
//   [UUID: Player UUID]        (16바이트, 1.19.3+ / protocol >= 761)

// MCLoginInfo 는 Login Start 패킷에서 추출한 정보입니다.
type MCLoginInfo struct {
	PlayerName      string
	PlayerUUID      string // "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" 형식
	ProtocolVersion int    // 핸드셰이크에서 추출한 프로토콜 버전
}

// ParseLoginFromPeekData 는 peek 데이터에서 핸드셰이크 다음 패킷(Login Start)을 파싱합니다.
// peek 데이터는 첫 번째 패킷(Handshake)과 두 번째 패킷(Login Start)을 포함할 수 있습니다.
// 첫 번째 패킷의 consumed 길이 이후가 Login Start 패킷입니다.
func ParseLoginFromPeekData(data []byte) (*MCLoginInfo, error) {
	if len(data) < 7 {
		return nil, errors.New("peek data too short")
	}

	r := &byteReader{data: data, pos: 0}

	// ── 1패킷: Handshake ──
	handshakePktLen, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("read handshake packet length: %w", err)
	}
	if handshakePktLen < 1 || handshakePktLen > 1024 {
		return nil, fmt.Errorf("invalid handshake packet length: %d", handshakePktLen)
	}

	// 핸드셰이크에서 ProtocolVersion 추출
	handshakeBodyStart := r.pos
	_, _ = ReadVarInt(r) // Packet ID (0x00)
	protoVer, _ := ReadVarInt(r)
	r.pos = handshakeBodyStart // 원위치

	// Handshake 패킷 본체 스킵
	if r.pos+handshakePktLen > len(data) {
		return nil, errors.New("handshake packet truncated, login start not available")
	}
	r.pos += handshakePktLen

	// ── 2패킷: Login Start ──
	if r.pos >= len(data) {
		return nil, errors.New("no second packet in peek data")
	}

	loginPktLen, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("read login packet length: %w", err)
	}
	if loginPktLen < 1 || loginPktLen > 1024 {
		return nil, fmt.Errorf("invalid login packet length: %d", loginPktLen)
	}

	// Packet ID (VarInt) — must be 0x00 (Login Start)
	pktID, err := ReadVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("read packet id: %w", err)
	}
	if pktID != 0x00 {
		return nil, fmt.Errorf("not a login start packet: id=0x%02x", pktID)
	}

	// Player Name (String, max 16)
	name, err := ReadString(r, 16)
	if err != nil {
		return nil, fmt.Errorf("read player name: %w", err)
	}

	info := &MCLoginInfo{
		PlayerName:      name,
		ProtocolVersion: protoVer,
	}

	// UUID (16 bytes) — 프로토콜 761 (1.19.3+) 이상에서 패킷에 포함
	if r.pos+16 <= len(data) {
		uuidBytes, err := r.ReadBytes(16)
		if err == nil {
			info.PlayerUUID = formatUUID(uuidBytes)
		}
	}

	return info, nil
}

// formatUUID 는 16바이트를 "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" 형식으로 변환합니다.
func formatUUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// readerByteReader 는 io.Reader를 io.ByteReader로 래핑하면서
// 읽은 바이트를 기록합니다. (VarInt 길이를 raw에 포함시키기 위함)
type readerByteReader struct {
	r       io.Reader
	written []byte
}

func (rb *readerByteReader) ReadByte() (byte, error) {
	buf := []byte{0}
	n, err := rb.r.Read(buf)
	if n == 1 {
		rb.written = append(rb.written, buf[0])
	}
	return buf[0], err
}

// ──────────────────────────────────────────────
// bufio 기반 패킷 읽기
// ──────────────────────────────────────────────

// ReadInitialPackets 는 bufio.Reader에서 Handshake + (Login Start 또는 Status Request) 두 패킷을
// 순차적으로 읽어 원본 바이트와 로그인 정보를 반환합니다.
// NextState==2(Login)인 경우에만 Login Start를 파싱하여 플레이어 정보를 추출합니다.
// NextState==1(Status)인 경우 두 번째 패킷(Status Request)만 읽고 nil을 반환합니다.
func ReadInitialPackets(reader *bufio.Reader) (raw []byte, loginInfo *MCLoginInfo, err error) {
	// ── 1패킷: Handshake 읽기 ──
	pkt1ID, _, pkt1Raw, err := ReadFullPacketFromBufio(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read handshake packet: %w", err)
	}
	if pkt1ID != 0x00 {
		return nil, nil, fmt.Errorf("expected handshake packet, got id=0x%02x", pkt1ID)
	}

	var allRaw []byte
	allRaw = append(allRaw, pkt1Raw...)

	// 핸드셰이크에서 ProtocolVersion, NextState 추출
	protoVer := 0
	nextState := 0
	{
		r := &byteReader{data: pkt1Raw, pos: 0}
		ReadVarInt(r) // packet length
		ReadVarInt(r) // packet id
		protoVer, _ = ReadVarInt(r)
		// skip Server Address (String)
		strLen, _ := ReadVarInt(r)
		r.pos += strLen
		// skip Server Port (UShort)
		r.pos += 2
		nextState, _ = ReadVarInt(r)
	}

	// ── 2패킷 읽기 ──
	pkt2ID, pkt2Payload, pkt2Raw, err := ReadFullPacketFromBufio(reader)
	if err != nil {
		return allRaw, nil, fmt.Errorf("read second packet: %w", err)
	}
	allRaw = append(allRaw, pkt2Raw...)

	// Status 요청이면 플레이어 정보 없이 반환 (패스스루)
	if nextState == 1 {
		return allRaw, nil, nil
	}

	// Login 요청이면 Login Start 파싱 (Packet ID == 0x00)
	if pkt2ID != 0x00 {
		return allRaw, nil, fmt.Errorf("expected login start packet, got id=0x%02x", pkt2ID)
	}

	// Login Start payload에서 플레이어 이름 + UUID 추출
	r := &byteReader{data: pkt2Payload, pos: 0}
	name, err := ReadString(r, 16)
	if err != nil {
		return allRaw, nil, fmt.Errorf("read player name: %w", err)
	}

	info := &MCLoginInfo{
		PlayerName:      name,
		ProtocolVersion: protoVer,
	}

	if r.pos+16 <= len(pkt2Payload) {
		uuidBytes, err := r.ReadBytes(16)
		if err == nil {
			info.PlayerUUID = formatUUID(uuidBytes)
		}
	}

	return allRaw, info, nil
}

// ReadFullPacketFromBufio 는 bufio.Reader에서 하나의 MC 패킷을 읽습니다.
// 반환값: packetID, payload, rawBytes (패킷 전체 원본), error
func ReadFullPacketFromBufio(reader *bufio.Reader) (packetID int, payload []byte, raw []byte, err error) {
	// Packet Length (VarInt)
	br := &readerByteReader{r: reader}
	pktLen, err := ReadVarInt(br)
	if err != nil {
		return 0, nil, nil, err
	}
	if pktLen < 1 || pktLen > 1<<21 { // 2MB 상한
		return 0, nil, nil, fmt.Errorf("invalid packet length: %d", pktLen)
	}

	// 패킷 본체 읽기
	body := make([]byte, pktLen)
	if _, err := io.ReadFull(reader, body); err != nil {
		return 0, nil, nil, fmt.Errorf("read packet body: %w", err)
	}

	// raw = VarInt length bytes + body
	rawBuf := br.written
	rawBuf = append(rawBuf, body...)

	// Packet ID 파싱
	br2 := &byteReader{data: body, pos: 0}
	pktID, err := ReadVarInt(br2)
	if err != nil {
		return 0, nil, rawBuf, fmt.Errorf("read packet id: %w", err)
	}

	return pktID, body[br2.pos:], rawBuf, nil
}

// ──────────────────────────────────────────────
// 서버 응답 패킷 생성 유틸
// ──────────────────────────────────────────────

// WriteVarInt 는 VarInt를 writer에 기록합니다.
func WriteVarInt(w io.Writer, val int) error {
	buf := make([]byte, 0, 5)
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
	_, err := w.Write(buf)
	return err
}

// WriteString 는 MC 프로토콜의 String (VarInt length + UTF-8 bytes)을 writer에 기록합니다.
func WriteString(w io.Writer, s string) error {
	if err := WriteVarInt(w, len(s)); err != nil {
		return err
	}
	_, err := w.Write([]byte(s))
	return err
}

// MarshalPacket 은 Packet ID와 payload를 포함한 완전한 MC 패킷 바이트를 생성합니다.
// 반환값: [VarInt: Packet Length][VarInt: Packet ID][Payload...]
func MarshalPacket(packetID int, payload []byte) []byte {
	// packet body = VarInt(Packet ID) + payload
	idBuf := &bytes.Buffer{}
	WriteVarInt(idBuf, packetID)
	body := append(idBuf.Bytes(), payload...)

	pkt := &bytes.Buffer{}
	WriteVarInt(pkt, len(body))
	pkt.Write(body)
	return pkt.Bytes()
}

// BuildDisconnectLoginPacket 은 Login 단계에서의 Disconnect 패킷을 생성합니다.
// Packet ID: 0x00, Payload: JSON chat component (String)
// MC 프로토콜의 Disconnect (login) 패킷입니다.
func BuildDisconnectLoginPacket(reasonJSON string) []byte {
	payloadBuf := &bytes.Buffer{}
	WriteString(payloadBuf, reasonJSON)
	return MarshalPacket(0x00, payloadBuf.Bytes())
}
