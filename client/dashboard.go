// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	_ "embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed client-admin.html
var clientAdminHTML []byte

// ──────────────────────────────────────────────
// 클라이언트 로컬 대시보드 (localhost 전용)
// ──────────────────────────────────────────────

// PlayerInfo 는 접속 중인 플레이어 정보입니다.
type PlayerInfo struct {
	Name        string
	UUID        string
	Addr        string
	ConnectedAt time.Time
}

// ClientDashboard 는 클라이언트 로컬 대시보드 서버입니다.
type ClientDashboard struct {
	assignedPort uint16
	serverAddr   string
	localAddr    string

	// 플레이어 추적
	players sync.Map // playerName -> *PlayerInfo

	// 트래픽 카운터 (누적)
	bytesIn  atomic.Int64
	bytesOut atomic.Int64

	// 스냅샷 (interval 계산용)
	lastBytesIn  atomic.Int64
	lastBytesOut atomic.Int64

	// 하트비트 추적
	lastHeartbeat atomic.Int64 // UnixNano

	// 연결 상태
	connected atomic.Bool

	// 연결 이력
	connectedAt time.Time
	listener    net.Listener
}

// NewClientDashboard 는 새 클라이언트 대시보드를 생성합니다.
func NewClientDashboard(serverAddr, localAddr string) *ClientDashboard {
	return &ClientDashboard{
		serverAddr:  serverAddr,
		localAddr:   localAddr,
		connectedAt: time.Now(),
	}
}

// SetAssignedPort 는 할당된 포트를 설정합니다.
func (cd *ClientDashboard) SetAssignedPort(port uint16) {
	cd.assignedPort = port
}

// AddPlayer 는 플레이어를 추가합니다.
func (cd *ClientDashboard) AddPlayer(name, uuid, addr string) {
	cd.players.Store(name, &PlayerInfo{
		Name:        name,
		UUID:        uuid,
		Addr:        addr,
		ConnectedAt: time.Now(),
	})
}

// RemovePlayer 는 플레이어를 제거합니다.
func (cd *ClientDashboard) RemovePlayer(name string) {
	cd.players.Delete(name)
}

// AddBytesIn 은 수신 바이트를 누적합니다.
func (cd *ClientDashboard) AddBytesIn(n int64) {
	cd.bytesIn.Add(n)
}

// AddBytesOut 은 송신 바이트를 누적합니다.
func (cd *ClientDashboard) AddBytesOut(n int64) {
	cd.bytesOut.Add(n)
}

// UpdateHeartbeat 은 하트비트 시간을 갱신합니다.
func (cd *ClientDashboard) UpdateHeartbeat() {
	cd.lastHeartbeat.Store(time.Now().UnixNano())
}

// SetConnected 는 서버 연결 상태를 설정합니다.
func (cd *ClientDashboard) SetConnected(v bool) {
	cd.connected.Store(v)
}

// Start 는 대시보드 서버를 시작합니다.
func (cd *ClientDashboard) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/client/status", cd.handleStatus)
	mux.HandleFunc("/api/client/players", cd.handlePlayers)
	mux.HandleFunc("/api/health", cd.handleHealth)
	mux.HandleFunc("/dashboard", cd.handleDashboardPage)
	mux.HandleFunc("/dashboard/", cd.handleDashboardPage)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	cd.listener = ln

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[ClientDashboard] 서버 에러: %v", err)
		}
	}()

	return nil
}

// ──────────────────────────────────────────────
// API 핸들러
// ──────────────────────────────────────────────

// ClientStatus 는 클라이언트 상태 응답입니다.
type ClientStatus struct {
	AssignedPort  uint16       `json:"assigned_port"`
	ServerAddr    string       `json:"server_addr"`
	LocalAddr     string       `json:"local_addr"`
	PlayerCount   int32        `json:"player_count"`
	Players       []PlayerData `json:"players"`
	BytesIn       int64        `json:"bytes_in"`
	BytesOut      int64        `json:"bytes_out"`
	IntervalIn    int64        `json:"interval_in"`
	IntervalOut   int64        `json:"interval_out"`
	LastHeartbeat string       `json:"last_heartbeat"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	Connected     bool         `json:"connected"`
}

// PlayerData 는 API 응답용 플레이어 정보입니다.
type PlayerData struct {
	Name        string `json:"name"`
	UUID        string `json:"uuid"`
	ConnectedAt string `json:"connected_at"`
}

func (cd *ClientDashboard) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 스냅샷 기반 interval 계산
	curIn := cd.bytesIn.Load()
	curOut := cd.bytesOut.Load()
	lastIn := cd.lastBytesIn.Load()
	lastOut := cd.lastBytesOut.Load()
	cd.lastBytesIn.Store(curIn)
	cd.lastBytesOut.Store(curOut)

	var players []PlayerData
	var count int32
	cd.players.Range(func(_, v interface{}) bool {
		p := v.(*PlayerInfo)
		players = append(players, PlayerData{
			Name:        p.Name,
			UUID:        p.UUID,
			ConnectedAt: p.ConnectedAt.Format(time.RFC3339),
		})
		count++
		return true
	})
	if players == nil {
		players = []PlayerData{}
	}

	lastHB := time.Unix(0, cd.lastHeartbeat.Load())
	lastHBStr := ""
	if cd.lastHeartbeat.Load() > 0 {
		lastHBStr = lastHB.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ClientStatus{
		AssignedPort:  cd.assignedPort,
		ServerAddr:    cd.serverAddr,
		LocalAddr:     cd.localAddr,
		PlayerCount:   count,
		Players:       players,
		BytesIn:       curIn,
		BytesOut:      curOut,
		IntervalIn:    curIn - lastIn,
		IntervalOut:   curOut - lastOut,
		LastHeartbeat: lastHBStr,
		UptimeSeconds: int64(time.Since(cd.connectedAt).Seconds()),
		Connected:     cd.connected.Load(),
	})
}

func (cd *ClientDashboard) handlePlayers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var players []PlayerData
	cd.players.Range(func(_, v interface{}) bool {
		p := v.(*PlayerInfo)
		players = append(players, PlayerData{
			Name:        p.Name,
			UUID:        p.UUID,
			ConnectedAt: p.ConnectedAt.Format(time.RFC3339),
		})
		return true
	})
	if players == nil {
		players = []PlayerData{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(players)
}

// handleDashboardPage 는 대시보드 HTML을 반환합니다.
func (cd *ClientDashboard) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(clientAdminHTML)
}

// handleHealth 는 헬스체크 엔드포인트입니다. 서버가 살아있으면 1x1 투명 GIF를 반환합니다.
func (cd *ClientDashboard) handleHealth(w http.ResponseWriter, r *http.Request) {
	// 1x1 투명 GIF 이미지
	gif := []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, // GIF89a
		0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
		0xff, 0xff, 0xff, 0x00, 0x00, 0x00,
		0x21, 0xf9, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x2c, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x01, 0x00, 0x00,
		0x02, 0x02, 0x44, 0x01, 0x00,
		0x3b,
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Write(gif)
}
