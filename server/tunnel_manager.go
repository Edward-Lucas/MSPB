// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mspb/shared"
)

// ──────────────────────────────────────────────
// 터널 매니저: 클라이언트 연결 관리
// ──────────────────────────────────────────────

// TunnelManager 는 클라이언트의 터널 연결을 관리합니다.
//
// 구조:
//
//	클라이언트 TCP → yamux.Server (control) → control stream + data streams
type TunnelManager struct {
	portTable *PortTable
	publicIP  string // 서버의 공인 IP
}

// NewTunnelManager 은 새로운 터널 매니저를 생성합니다.
func NewTunnelManager(pt *PortTable, publicIP string) *TunnelManager {
	return &TunnelManager{portTable: pt, publicIP: publicIP}
}

// botResponse 는 비-yamux 연결(봇/스캐너)에게 보내는 식별 메시지입니다.
const botResponse = "MSPB - MiFun Server Proxy Bridge\r\nThis is a private tunnel endpoint, not a public service.\r\n"

// connWithReplacedReader 는 읽기만 다른 reader로 대체하고, 쓰기/닫기는 원본 conn을 사용하는 래퍼입니다.
type connWithReplacedReader struct {
	reader io.Reader
	conn   net.Conn
}

func (w *connWithReplacedReader) Read(p []byte) (int, error)  { return w.reader.Read(p) }
func (w *connWithReplacedReader) Write(p []byte) (int, error) { return w.conn.Write(p) }
func (w *connWithReplacedReader) Close() error                { return w.conn.Close() }
func (w *connWithReplacedReader) LocalAddr() net.Addr         { return w.conn.LocalAddr() }
func (w *connWithReplacedReader) RemoteAddr() net.Addr        { return w.conn.RemoteAddr() }
func (w *connWithReplacedReader) SetDeadline(t time.Time) error      { return w.conn.SetDeadline(t) }
func (w *connWithReplacedReader) SetReadDeadline(t time.Time) error  { return w.conn.SetReadDeadline(t) }
func (w *connWithReplacedReader) SetWriteDeadline(t time.Time) error { return w.conn.SetWriteDeadline(t) }

// HandleClientConnection 은 클라이언트의 최초 TCP 연결을 처리합니다.
//
// 흐름:
//  1. 클라이언트 TCP 연결 수락
//  2. 첫 바이트 peek → yamux 프로토콜 검증 (비-yamux 연결은 식별 메시지 후 종료)
//  3. yamux 서버 세션 생성 (control 통로)
//  4. control 스트림에서 Handshake 프레임 수신
//  5. 포트 할당 → PortAssign 프레임 응답
//  6. 하트비트 고루틴 시작
//  7. 데이터 스트림 수신 대기 (외부 접속 → data stream → 로컬 MC)
func (tm *TunnelManager) HandleClientConnection(conn net.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("[Tunnel] 클라이언트 연결: %s", remoteAddr)

	// ── yamux 프로토콜 사전 검증 ──
	// 첫 1바이트를 미리 읽어서 yamux version(0x00)인지 확인한다.
	// 봇/스캐너가 보내는 HTTP(SOCKS5 등) 요청은 첫 바이트가 0x00이 아니므로
	// 여기서 빠르게 걸러낼 수 있다.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var versionBuf [1]byte
	if _, err := io.ReadFull(conn, versionBuf[:]); err != nil {
		log.Printf("[Tunnel] 첫 바이트 읽기 실패: addr=%s err=%v", remoteAddr, err)
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // 데드라인 제거

	if versionBuf[0] != 0 {
		// 비-yamux 프로토콜 — 식별 메시지 전송 후 종료
		log.Printf("[Tunnel] 비-yamux 프로토콜 감지 (0x%02X): addr=%s", versionBuf[0], remoteAddr)
		_, _ = conn.Write([]byte(botResponse))
		conn.Close()
		return
	}

	// yamux 정상 경로 — 읽은 바이트를 되돌려서 yamux에 전달
	wrappedConn := &connWithReplacedReader{
		reader: io.MultiReader(bytes.NewReader(versionBuf[:]), conn),
		conn:   conn,
	}

	// yamux 서버 세션 생성 (이 연결이 control + data 통로의 기반이 됨)
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.StreamOpenTimeout = shared.YamuxStreamOpenTimeout
	yamuxCfg.StreamCloseTimeout = shared.YamuxStreamCloseTimeout
	yamuxCfg.KeepAliveInterval = shared.YamuxKeepAliveInterval
	yamuxCfg.MaxStreamWindowSize = 1024 * 1024 // 1MB

	session, err := yamux.Server(wrappedConn, yamuxCfg)
	if err != nil {
		log.Printf("[Tunnel] yamux 세션 생성 실패: %v", err)
		conn.Close()
		return
	}

	// 제어 스트림 수신 (클라이언트가 첫 스트림을 열면 control)
	controlStream, err := session.AcceptStream()
	if err != nil {
		log.Printf("[Tunnel] 제어 스트림 수신 실패: %v", err)
		session.Close()
		return
	}

	// 핸드셰이크 프레임 읽기
	_ = controlStream.SetReadDeadline(time.Now().Add(10 * time.Second))
	frame, err := shared.ReadFrame(controlStream)
	if err != nil || frame.Type != shared.MsgHandshake {
		log.Printf("[Tunnel] 핸드셰이크 실패: %v", err)
		shared.WriteFrame(controlStream, shared.Frame{
			Type:    shared.MsgPortAssign,
			Payload: (&shared.PortAssignment{Success: false, Message: "invalid handshake"}).Marshal(),
		})
		controlStream.Close()
		session.Close()
		return
	}
	_ = controlStream.SetReadDeadline(time.Time{}) // 데드라인 제거

	req, err := shared.UnmarshalHandshakeRequest(frame.Payload)
	if err != nil {
		log.Printf("[Tunnel] 핸드셰이크 파싱 실패: %v", err)
		shared.WriteFrame(controlStream, shared.Frame{
			Type:    shared.MsgPortAssign,
			Payload: (&shared.PortAssignment{Success: false, Message: "bad handshake payload"}).Marshal(),
		})
		controlStream.Close()
		session.Close()
		return
	}

	clientID := fmt.Sprintf("%s-%d", remoteAddr, time.Now().UnixNano())

	// 포트 할당
	port, err := tm.portTable.AllocatePort(clientID)
	if err != nil {
		log.Printf("[Tunnel] 포트 할당 실패: %v", err)
		shared.WriteFrame(controlStream, shared.Frame{
			Type:    shared.MsgPortAssign,
			Payload: (&shared.PortAssignment{Success: false, Message: err.Error()}).Marshal(),
		})
		controlStream.Close()
		session.Close()
		return
	}

	// 세션에 yamux session 등록
	if sess, ok := tm.portTable.GetSession(port); ok {
		sess.DataSession = session
	}

	// 성공 응답 (공인 IP 포함)
	assignResp := &shared.PortAssignment{
		AssignedPort: port,
		Success:      true,
		ServerIP:     tm.publicIP,
		Message:      fmt.Sprintf("서버 개방 성공! 접속 주소: %s:%d", tm.publicIP, port),
	}
	if err := shared.WriteFrame(controlStream, shared.Frame{
		Type:    shared.MsgPortAssign,
		Payload: assignResp.Marshal(),
	}); err != nil {
		log.Printf("[Tunnel] 포트 할당 응답 전송 실패: %v", err)
		tm.cleanupSession(port, controlStream)
		return
	}

	log.Printf("[Tunnel] 세션 활성화: client=%s port=%d", clientID, port)

	// 하트비트 처리 고루틴
	go tm.handleHeartbeat(controlStream, port)

	// 외부 리스너 → 데이터 스트림 브릿지 시작
	go tm.acceptExternalConnections(port, session, req.ServerPort)
}

// handleHeartbeat 은 제어 스트림에서 하트비트를 주고받습니다.
func (tm *TunnelManager) handleHeartbeat(stream net.Conn, port uint16) {
	defer tm.cleanupSession(port, stream)

	for {
		// 읽기 타임아웃 = 하트비트 타임아웃
		_ = stream.SetReadDeadline(time.Now().Add(shared.HeartbeatTimeout))
		frame, err := shared.ReadFrame(stream)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				log.Printf("[Heartbeat] 타임아웃: port=%d", port)
			} else {
				log.Printf("[Heartbeat] 읽기 에러: port=%d err=%v", port, err)
			}
			return
		}

		switch frame.Type {
		case shared.MsgHeartbeat:
			// 클라이언트 Ping → Pong 응답
			tm.portTable.UpdateHeartbeat(port)
			if err := shared.SendHeartbeatACK(stream); err != nil {
				log.Printf("[Heartbeat] Pong 전송 실패: port=%d err=%v", port, err)
				return
			}
		case shared.MsgHeartbeatACK:
			// 클라이언트 Pong → 하트비트 갱신
			tm.portTable.UpdateHeartbeat(port)
		case shared.MsgClose:
			log.Printf("[Heartbeat] 클라이언트 종료 요청: port=%d", port)
			return
		default:
			// 제어 스트림에서 예상치 못한 메시지 — 무시
			log.Printf("[Heartbeat] 알 수 없는 메시지: type=0x%02x port=%d", frame.Type, port)
		}
	}
}

// acceptExternalConnections 은 외부 접속을 수신하여 데이터 스트림으로 전달합니다.
func (tm *TunnelManager) acceptExternalConnections(port uint16, dataSession *yamux.Session, localPort uint16) {
	sess, ok := tm.portTable.GetSession(port)
	if !ok {
		return
	}

	listener := sess.Listener
	log.Printf("[Data] 외부 리스너 시작: port=%d", port)

	for {
		externalConn, err := listener.Accept()
		if err != nil {
			if sess.Closed.Load() {
				return // 정상 종료
			}
			log.Printf("[Data] 외부 수신 에러: port=%d err=%v", port, err)
			continue
		}

		go tm.handleExternalConnection(externalConn, dataSession, port, localPort)
	}
}

// handleExternalConnection 은 하나의 외부 접속을 처리합니다.
//
// 흐름:
//  1. 첫 패킷 Peek → MC 핸드셰이크 검증 (Hybrid Passthrough)
//  2. 검증 실패 시 연결 차단
//  3. 검증 성공 시 yamux 데이터 스트림 열기 → 양방향 복사
func (tm *TunnelManager) handleExternalConnection(externalConn net.Conn, dataSession *yamux.Session, port uint16, localPort uint16) {
	defer externalConn.Close()

	remoteAddr := externalConn.RemoteAddr().String()

	// Step 1: bufio.Reader로 패킷 경계까지 정확히 읽기 (모드드 클라이언트 대응)
	bufReader := bufio.NewReaderSize(externalConn, shared.PeekBufferSize)
	_ = externalConn.SetReadDeadline(time.Now().Add(time.Duration(shared.PeekTimeoutSeconds) * time.Second))
	initialData, loginInfo, readErr := shared.ReadInitialPackets(bufReader)
	_ = externalConn.SetReadDeadline(time.Time{})
	if readErr != nil {
		log.Printf("[Peek] MC 프로토콜 아님 (차단): addr=%s reason=%v", remoteAddr, readErr)
		return // 연결을 조용히 닫음 — 악용 차단
	}

	var playerName, playerUUID string
	protoVer := 0
	if loginInfo != nil {
		playerName = loginInfo.PlayerName
		playerUUID = loginInfo.PlayerUUID
		protoVer = loginInfo.ProtocolVersion
	}

	log.Printf("[Peek] MC 핸드셰이크 확인: addr=%s proto=%d", remoteAddr, protoVer)

	// 세션 조회 (플레이어 추적 + 트래픽 카운터 공용)
	sess, sessOK := tm.portTable.GetSession(port)

	// Step 2-1: 로그인 접속만 인원 수 체크 (Status는 제외)
	if loginInfo != nil && sessOK {
		if !sess.IncrementPlayer(shared.MaxPlayersPerClient) {
			log.Printf("[Data] 인원 초과 거부: addr=%s port=%d (현재 %d/%d)",
				remoteAddr, port, sess.PlayerCount.Load(), shared.MaxPlayersPerClient)
			disconnectPkt := shared.BuildDisconnectLoginPacket(
				`{"text":"서버가 가득 찼습니다. (최대 인원: ` +
					fmt.Sprintf("%d", shared.MaxPlayersPerClient) + `명)` +
					`","color":"red"}`)
			_, _ = externalConn.Write(disconnectPkt)
			return
		}
		defer sess.DecrementPlayer()
		if playerName != "" {
			sess.AddPlayer(playerName, playerUUID, remoteAddr)
			defer sess.RemovePlayer(playerName)
		}
		log.Printf("[Data] 플레이어 접속 허용: addr=%s port=%d (%d/%d)",
			remoteAddr, port, sess.PlayerCount.Load(), shared.MaxPlayersPerClient)
	}

	// Step 3: yamux 데이터 스트림 열기
	dataStream, err := dataSession.OpenStream()
	if err != nil {
		log.Printf("[Data] yamux 스트림 열기 실패: port=%d err=%v", port, err)
		return
	}
	defer dataStream.Close()

	// 초기 패킷 데이터를 먼저 데이터 스트림에 전달 (놓치면 안 됨)
	if _, err := dataStream.Write(initialData); err != nil {
		log.Printf("[Data] 초기 패킷 전달 실패: port=%d err=%v", port, err)
		return
	}
	initLen := int64(len(initialData))
	if sessOK {
		sess.BytesIn.Add(initLen)
	}
	globalBytesIn.Add(initLen)

	// Step 4: 양방향 복사 (external ↔ yamux data stream)
	// bufio.Reader를 통해 읽어야 버퍼에 남은 바이트도 함께 전달됨
	log.Printf("[Data] 트래픽 브릿지 시작: addr=%s port=%d", remoteAddr, port)

	done := make(chan struct{}, 2)
	go func() {
		cw := &countWriter{dst: dataStream, counter: &sess.BytesIn, globalCounter: &globalBytesIn}
		io.Copy(cw, bufReader) // external(bufio.Reader) → tunnel
		done <- struct{}{}
	}()
	go func() {
		cw := &countWriter{dst: externalConn, counter: &sess.BytesOut, globalCounter: &globalBytesOut}
		io.Copy(cw, dataStream) // tunnel → external
		done <- struct{}{}
	}()

	<-done // 하나라도 끝나면 종료
	log.Printf("[Data] 트래픽 브릿지 종료: addr=%s port=%d", remoteAddr, port)
}

// countWriter wraps an io.Writer and atomically counts bytes written.
type countWriter struct {
	dst           io.Writer
	counter       *atomic.Int64
	globalCounter *atomic.Int64 // optional: 전역 누적 카운터
}

func (w *countWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.counter.Add(int64(n))
	if w.globalCounter != nil {
		w.globalCounter.Add(int64(n))
	}
	return n, err
}

// cleanupSession 은 세션과 관련 리소스를 정리합니다.
func (tm *TunnelManager) cleanupSession(port uint16, controlStream net.Conn) {
	if sess, ok := tm.portTable.GetSession(port); ok {
		tm.portTable.removeSession(sess) // 맵 제거 + 리스너/yamux 정리
	}
	if controlStream != nil {
		controlStream.Close()
	}
}
