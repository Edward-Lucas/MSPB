// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package shared

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// ──────────────────────────────────────────────
// 프레임 프로토콜: yamux 스트림 위의 단위 메시지
// ──────────────────────────────────────────────

// MsgType 은 프레임의 종류를 나타냅니다.
type MsgType byte

const (
	MsgHandshake    MsgType = 0x01 // client→server 최초 연결 요청
	MsgPortAssign   MsgType = 0x02 // server→client 포트 할당 응답
	MsgHeartbeat    MsgType = 0x03 // 양방향 Ping
	MsgHeartbeatACK MsgType = 0x04 // 양방향 Pong
	MsgClose        MsgType = 0x05 // 세션 종료 요청
)

// Frame 은 공통 프레임 구조입니다.
//
//	+--------+----------+--------+
//	| Type   | Length   | Payload|
//	| 1 byte | 4 bytes  | N bytes|
//	+--------+----------+--------+
type Frame struct {
	Type    MsgType
	Payload []byte
}

const frameHeaderSize = 1 + 4 // Type(1) + Length(4)

// WriteFrame 은 프레임을 writer 에 기록합니다.
func WriteFrame(w io.Writer, f Frame) error {
	buf := make([]byte, frameHeaderSize+len(f.Payload))
	buf[0] = byte(f.Type)
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(f.Payload)))
	copy(buf[5:], f.Payload)
	_, err := w.Write(buf)
	return err
}

// ReadFrame 은 reader 로부터 프레임을 읽습니다.
func ReadFrame(r io.Reader) (Frame, error) {
	hdr := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(hdr[1:5])
	if length > 1<<20 { // 1MB 상한
		return Frame{}, errors.New("frame too large")
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Type: MsgType(hdr[0]), Payload: payload}, nil
}

// ──────────────────────────────────────────────
// 핸드셰이크 페이로드
// ──────────────────────────────────────────────

// HandshakeRequest 는 클라이언트→서버 전송 시 사용됩니다.
type HandshakeRequest struct {
	Token      string // 인증 토큰 (추후 확장)
	ServerPort uint16 // 로컬 MC 서버 포트 (기본 25565)
}

// Marshal 을 바이트로 직렬화합니다.
// 포맷: [token_len:2][token:N][server_port:2]
func (h *HandshakeRequest) Marshal() []byte {
	tokenBytes := []byte(h.Token)
	buf := make([]byte, 2+len(tokenBytes)+2)
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(tokenBytes)))
	copy(buf[2:], tokenBytes)
	binary.BigEndian.PutUint16(buf[2+len(tokenBytes):], h.ServerPort)
	return buf
}

// UnmarshalHandshakeRequest 는 바이트에서 파싱합니다.
func UnmarshalHandshakeRequest(data []byte) (*HandshakeRequest, error) {
	if len(data) < 4 {
		return nil, errors.New("handshake payload too short")
	}
	tokenLen := binary.BigEndian.Uint16(data[0:2])
	if len(data) < int(2+tokenLen+2) {
		return nil, errors.New("handshake payload truncated")
	}
	token := string(data[2 : 2+tokenLen])
	port := binary.BigEndian.Uint16(data[2+tokenLen:])
	return &HandshakeRequest{Token: token, ServerPort: port}, nil
}

// PortAssignment 은 서버→클라이언트 응답입니다.
type PortAssignment struct {
	AssignedPort uint16
	Success      bool
	ServerIP     string // 서버의 공인 IP (외부 접속 주소용)
	Message      string
}

// Marshal 직렬화
// 포맷: [port:2][success:1][server_ip_len:2][server_ip:N][msg_len:2][msg:M]
func (p *PortAssignment) Marshal() []byte {
	ipBytes := []byte(p.ServerIP)
	msgBytes := []byte(p.Message)
	var flag byte
	if p.Success {
		flag = 1
	}
	buf := make([]byte, 2+1+2+len(ipBytes)+2+len(msgBytes))
	binary.BigEndian.PutUint16(buf[0:2], p.AssignedPort)
	buf[2] = flag
	binary.BigEndian.PutUint16(buf[3:5], uint16(len(ipBytes)))
	copy(buf[5:], ipBytes)
	binary.BigEndian.PutUint16(buf[5+len(ipBytes):], uint16(len(msgBytes)))
	copy(buf[5+len(ipBytes)+2:], msgBytes)
	return buf
}

// UnmarshalPortAssignment 역직렬화
func UnmarshalPortAssignment(data []byte) (*PortAssignment, error) {
	if len(data) < 5 {
		return nil, errors.New("port assignment payload too short")
	}
	port := binary.BigEndian.Uint16(data[0:2])
	success := data[2] == 1
	ipLen := binary.BigEndian.Uint16(data[3:5])
	if len(data) < int(5+ipLen+2) {
		return nil, errors.New("port assignment payload truncated at server_ip")
	}
	serverIP := string(data[5 : 5+ipLen])
	msgLen := binary.BigEndian.Uint16(data[5+ipLen:])
	if len(data) < int(5+ipLen+2+msgLen) {
		return nil, errors.New("port assignment payload truncated at message")
	}
	msg := string(data[5+ipLen+2 : 5+ipLen+2+msgLen])
	return &PortAssignment{AssignedPort: port, Success: success, ServerIP: serverIP, Message: msg}, nil
}

// ──────────────────────────────────────────────
// 하트비트 유틸
// ──────────────────────────────────────────────

const (
	HeartbeatInterval = 15 * time.Second
	HeartbeatTimeout  = 45 * time.Second // 3 missed beats
)

// SendHeartbeat 은 Ping 프레임을 전송합니다.
func SendHeartbeat(w io.Writer) error {
	return WriteFrame(w, Frame{Type: MsgHeartbeat})
}

// SendHeartbeatACK 은 Pong 프레임을 전송합니다.
func SendHeartbeatACK(w io.Writer) error {
	return WriteFrame(w, Frame{Type: MsgHeartbeatACK})
}

// ──────────────────────────────────────────────
// 네트워크 유틸
// ──────────────────────────────────────────────

// SetDeadline 은 읽기/쓰기 데드라인을 설정합니다.
func SetDeadline(conn net.Conn, d time.Duration) {
	if d > 0 {
		conn.SetDeadline(time.Now().Add(d))
	}
}

// ClearDeadline 은 데드라인을 제거합니다.
func ClearDeadline(conn net.Conn) {
	conn.SetDeadline(time.Time{})
}
