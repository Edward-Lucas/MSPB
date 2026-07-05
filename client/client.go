// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mspb/shared"
)

// ──────────────────────────────────────────────
// MSPB 클라이언트 (호스트 PC)
// ──────────────────────────────────────────────

// Config 는 클라이언트 설정입니다.
type Config struct {
	ServerAddr  string // 중앙 서버 주소 (예: "tunnel.example.com:19132")
	LocalMCAddr string // 로컬 MC 서버 주소 (예: "127.0.0.1:25565")
	Token       string // 인증 토큰 (추후 확장)
}

// DefaultConfig 는 기본 클라이언트 설정을 반환합니다.
func DefaultConfig() *Config {
	return &Config{
		ServerAddr:  shared.DefaultClientAddr,
		LocalMCAddr: shared.DefaultLocalMCAddr,
		Token:       "",
	}
}

// Client 는 MSPB 클라이언트 인스턴스입니다.
type Client struct {
	cfg           *Config
	controlConn   net.Conn
	controlStream net.Conn
	dataSession   *yamux.Session
	assignedPort  uint16
	done          chan struct{}
	dashboard     *ClientDashboard
}

// New 은 새로운 클라이언트를 생성합니다.
func New(cfg *Config) *Client {
	return &Client{
		cfg:       cfg,
		done:      make(chan struct{}),
		dashboard: NewClientDashboard(cfg.ServerAddr, cfg.LocalMCAddr),
	}
}

// Run 은 클라이언트를 실행합니다.
// 연결이 끊기면 자동 재연결을 시도합니다.
func (c *Client) Run() {
	for {
		if err := c.connectAndServe(); err != nil {
			c.dashboard.SetConnected(false)
			log.Printf("[Client] 연결 종료: %v", err)

			// done 신호 대기 (5초 또는 종료 시 즉시 해제)
			log.Printf("[Client] 5초 후 재연결 시도...")
			select {
			case <-c.done:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// connectAndServe 는 단일 연결 생명주기를 처리합니다.
func (c *Client) connectAndServe() error {
	// Step 1: 중앙 서버에 TCP 연결
	log.Printf("[Client] 중앙 서버 연결 중: %s", c.cfg.ServerAddr)
	conn, err := net.DialTimeout("tcp", c.cfg.ServerAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("서버 연결 실패: %w", err)
	}
	c.controlConn = conn
	defer func() {
		conn.Close()
		c.controlConn = nil
	}()

	// Step 2: yamux 클라이언트 세션 생성 (이 연결 = control + data 통로)
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.StreamOpenTimeout = shared.YamuxStreamOpenTimeout
	yamuxCfg.StreamCloseTimeout = shared.YamuxStreamCloseTimeout
	yamuxCfg.KeepAliveInterval = shared.YamuxKeepAliveInterval
	yamuxCfg.MaxStreamWindowSize = 1024 * 1024

	session, err := yamux.Client(conn, yamuxCfg)
	if err != nil {
		return fmt.Errorf("yamux 세션 생성 실패: %w", err)
	}
	c.dataSession = session
	defer func() {
		session.Close()
		c.dataSession = nil
	}()

	// Step 3: 제어 스트림 열기 (첫 스트림 = control)
	controlStream, err := session.OpenStream()
	if err != nil {
		return fmt.Errorf("제어 스트림 열기 실패: %w", err)
	}
	c.controlStream = controlStream
	defer func() {
		controlStream.Close()
		c.controlStream = nil
	}()

	// Step 4: 핸드셰이크 전송
	hsReq := &shared.HandshakeRequest{
		Token:      c.cfg.Token,
		ServerPort: 25565,
	}
	if err := shared.WriteFrame(controlStream, shared.Frame{
		Type:    shared.MsgHandshake,
		Payload: hsReq.Marshal(),
	}); err != nil {
		return fmt.Errorf("핸드셰이크 전송 실패: %w", err)
	}

	// Step 5: 포트 할당 응답 수신
	_ = controlStream.SetReadDeadline(time.Now().Add(10 * time.Second))
	respFrame, err := shared.ReadFrame(controlStream)
	if err != nil {
		return fmt.Errorf("포트 할당 응답 수신 실패: %w", err)
	}
	_ = controlStream.SetReadDeadline(time.Time{})

	if respFrame.Type != shared.MsgPortAssign {
		return fmt.Errorf("예상치 못한 응답: type=0x%02x", respFrame.Type)
	}

	assignResp, err := shared.UnmarshalPortAssignment(respFrame.Payload)
	if err != nil {
		return fmt.Errorf("포트 할당 응답 파싱 실패: %w", err)
	}

	if !assignResp.Success {
		return fmt.Errorf("포트 할당 거부: %s", assignResp.Message)
	}

	c.assignedPort = assignResp.AssignedPort
	c.dashboard.SetAssignedPort(c.assignedPort)
	c.dashboard.SetConnected(true)
	log.Printf("═══════════════════════════════════════════════")
	log.Printf("  서버 개방 성공!")
	if assignResp.ServerIP != "" {
		log.Printf("  외부 접속 주소: %s:%d", assignResp.ServerIP, c.assignedPort)
	} else {
		log.Printf("  외부 접속 주소: <서버IP>:%d", c.assignedPort)
	}
	log.Printf("  로컬 MC 서버: %s", c.cfg.LocalMCAddr)
	log.Printf("  대시보드: http://%s/dashboard", shared.DefaultDashboardAddr)
	log.Printf("═══════════════════════════════════════════════")

	// Step 6: 하트비트 고루틴 + 데이터 스트림 수신 고루틴 시작
	errCh := make(chan error, 2)

	go c.runHeartbeat(controlStream, errCh)
	go c.acceptDataStreams(errCh, c.cfg.LocalMCAddr)

	// 하나라도 에러 반환 시 종료
	return <-errCh
}

// runHeartbeat 은 제어 스트림에서 하트비트를 주고받습니다.
func (c *Client) runHeartbeat(stream net.Conn, errCh chan<- error) {
	ticker := time.NewTicker(shared.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			// Ping 전송
			if err := shared.SendHeartbeat(stream); err != nil {
				errCh <- fmt.Errorf("heartbeat send: %w", err)
				return
			}

			// Pong 수신 대기
			_ = stream.SetReadDeadline(time.Now().Add(shared.HeartbeatTimeout))
			frame, err := shared.ReadFrame(stream)
			if err != nil {
				errCh <- fmt.Errorf("heartbeat recv: %w", err)
				return
			}
			_ = stream.SetReadDeadline(time.Time{})

			if frame.Type != shared.MsgHeartbeatACK && frame.Type != shared.MsgHeartbeat {
				// 하트비트가 아닌 메시지 — 무시하지만 로그
				log.Printf("[Heartbeat] 예상치 못한 메시지: type=0x%02x", frame.Type)
			}
			// 대시보드에 하트비트 시간 갱신
			c.dashboard.UpdateHeartbeat()
		}
	}
}

// acceptDataStreams 는 서버에서 오는 데이터 스트림을 수신하여 로컬 MC 서버로 전달합니다.
func (c *Client) acceptDataStreams(errCh chan<- error, localAddr string) {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		// 서버가 외부 접속 시 열어주는 데이터 스트림 수신
		stream, err := c.dataSession.AcceptStream()
		if err != nil {
			errCh <- fmt.Errorf("data stream accept: %w", err)
			return
		}

		go c.handleDataStream(stream, localAddr)
	}
}

// handleDataStream 은 하나의 데이터 스트림을 로컬 MC 서버에 연결합니다.
func (c *Client) handleDataStream(stream net.Conn, localAddr string) {
	// 로컬 MC 서버에 연결
	localConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		log.Printf("[Data] 로컬 서버 연결 실패: %v", err)
		stream.Close()
		return
	}

	// bufio.Reader로 패킷 단위 읽기 준비
	reader := bufio.NewReaderSize(stream, 64*1024)

	// Handshake + Login Start 패킷을 순차적으로 읽어서 플레이어 정보 추출
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	initialData, loginInfo, err := shared.ReadInitialPackets(reader)
	if err != nil {
		log.Printf("[Data] 초기 패킷 읽기 실패: %v", err)
		stream.Close()
		localConn.Close()
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	// 플레이어 정보
	playerName := "unknown"
	playerUUID := ""
	if loginInfo != nil {
		playerName = loginInfo.PlayerName
		playerUUID = loginInfo.PlayerUUID
	}

	// 대시보드에 플레이어 등록
	remoteAddr := stream.RemoteAddr().String()
	c.dashboard.AddPlayer(playerName, playerUUID, remoteAddr)
	defer c.dashboard.RemovePlayer(playerName)

	// 접속 로그
	if playerUUID != "" {
		log.Printf("[Player] 접속: %s (%s)", playerName, playerUUID)
	} else {
		log.Printf("[Player] 접속: %s", playerName)
	}

	// 초기 패킷 데이터를 로컬 서버에 전달
	if _, err := localConn.Write(initialData); err != nil {
		log.Printf("[Data] 초기 데이터 전달 실패: %v", err)
		stream.Close()
		localConn.Close()
		return
	}
	// 초기 패킷 바이트도 트래픽에 포함
	c.dashboard.AddBytesOut(int64(len(initialData)))

	// 양방향 복사: yamux stream ↔ local MC server
	// reader에 남은 버퍼 데이터도 포함하여 전달
	inCounter := &byteCounter{add: c.dashboard.AddBytesIn}
	outCounter := &byteCounter{add: c.dashboard.AddBytesOut}

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(inCounter.Wrap(localConn), reader) // tunnel → local MC
		done <- struct{}{}
	}()
	go func() {
		io.Copy(outCounter.Wrap(stream), localConn) // local MC → tunnel
		done <- struct{}{}
	}()

	<-done
	stream.Close()
	localConn.Close()

	// 퇴장 로그
	if playerUUID != "" {
		log.Printf("[Player] 퇴장: %s (%s)", playerName, playerUUID)
	} else {
		log.Printf("[Player] 퇴장: %s", playerName)
	}
}

// Stop 은 클라이언트를 종료합니다.
func (c *Client) Stop() {
	c.dashboard.SetConnected(false)
	close(c.done)
	if c.controlConn != nil {
		// MsgClose 전송 시도
		if c.controlStream != nil {
			shared.WriteFrame(c.controlStream, shared.Frame{Type: shared.MsgClose})
		}
		c.controlConn.Close()
	}
}

// byteCounter 는 io.Writer를 감싸서 매 Write마다 바이트를 카운팅합니다.
type byteCounter struct {
	add func(int64)
}

type countingWriter struct {
	dst io.Writer
	add func(int64)
}

func (bc *byteCounter) Wrap(w io.Writer) *countingWriter {
	return &countingWriter{dst: w, add: bc.add}
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.dst.Write(p)
	cw.add(int64(n))
	return n, err
}
