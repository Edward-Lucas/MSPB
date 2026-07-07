// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// ──────────────────────────────────────────────
// 대시보드 HTTP 서버 (localhost 전용)
// ──────────────────────────────────────────────

// DashboardServer 는 관리용 웹 대시보드를 제공합니다.
type DashboardServer struct {
	portTable *PortTable
	listener  net.Listener
	webRoot   string
}

// NewDashboardServer 는 대시보드 서버를 생성합니다.
// webRoot 가 비어있으면 실행 파일 기준 web/ 디렉토리를 사용합니다.
func NewDashboardServer(pt *PortTable, webRoot string) *DashboardServer {
	if webRoot == "" {
		exe, err := os.Executable()
		if err == nil {
			webRoot = filepath.Join(filepath.Dir(exe), "web")
		} else {
			webRoot = "web"
		}
	}
	return &DashboardServer{portTable: pt, webRoot: webRoot}
}

// Start 는 대시보드 서버를 시작합니다.
// addr 는 반드시 loopback 주소여야 합니다 (예: "127.0.0.1:18080").
func (ds *DashboardServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", ds.handleHealth)
	mux.HandleFunc("/api/sessions", ds.handleSessions)
	mux.HandleFunc("/api/stats", ds.handleStats)
	mux.HandleFunc("/api/session/close", ds.handleCloseSession)
	mux.HandleFunc("/api/session/toggle-unlimited", ds.handleToggleUnlimited)
	mux.HandleFunc("/favicon.ico", ds.handleFavicon)
	mux.HandleFunc("/assets/", ds.handleAssets)
	mux.HandleFunc("/dashboard", ds.handleDashboardPage)
	mux.HandleFunc("/dashboard/", ds.handleDashboardPage)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ds.listener = ln

	log.Printf("[Dashboard] 대시보드 서버 시작: http://%s", addr)

	srv := &http.Server{
		Handler:      recoveryMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[Dashboard] 서버 에러: %v", err)
		}
	}()

	return nil
}

// recoveryMiddleware 는 panic으로부터 복구하고 로그를 남깁니다.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[Dashboard] panic recovered: %v\n%s", err, debug.Stack())
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handleHealth 는 헬스체크 엔드포인트입니다. 서버가 살아있으면 1x1 투명 GIF를 반환합니다.
func (ds *DashboardServer) handleHealth(w http.ResponseWriter, r *http.Request) {
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

// handleFavicon 는 favicon.ico 요청에 assets/icon.png를 서빙합니다.
func (ds *DashboardServer) handleFavicon(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(ds.webRoot, "assets", "icon.png"))
}

// handleAssets 는 /assets/ 경로의 정적 파일을 서빙합니다.
func (ds *DashboardServer) handleAssets(w http.ResponseWriter, r *http.Request) {
	// /assets/icon.png → assets/icon.png
	relPath := strings.TrimPrefix(r.URL.Path, "/assets/")
	fullPath := filepath.Join(ds.webRoot, "assets", relPath)

	// 경로 순회 방지
	assetsDir, _ := filepath.Abs(filepath.Join(ds.webRoot, "assets"))
	absFull, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFull, assetsDir+string(filepath.Separator)) && absFull != assetsDir {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, fullPath)
}

// handleDashboardPage 는 dashboard.html을 서빙합니다.
func (ds *DashboardServer) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	http.ServeFile(w, r, filepath.Join(ds.webRoot, "admin.html"))
}

// ──────────────────────────────────────────────
// API 응답 구조체
// ──────────────────────────────────────────────

// SessionInfo 는 API 응답용 세션 정보입니다.
type SessionInfo struct {
	ClientID         string        `json:"client_id"`
	AssignedPort     uint16        `json:"assigned_port"`
	PlayerCount      int32         `json:"player_count"`
	Players          []PlayerEntry `json:"players"`
	UnlimitedPlayers bool          `json:"unlimited_players"`
	BytesIn          int64         `json:"bytes_in"`     // cumulative
	BytesOut         int64         `json:"bytes_out"`    // cumulative
	IntervalIn       int64         `json:"interval_in"`  // bytes in last interval
	IntervalOut      int64         `json:"interval_out"` // bytes out last interval
	CreatedAt        string        `json:"created_at"`
	LastHeartbeat    string        `json:"last_heartbeat"`
	Closed           bool          `json:"closed"`
}

// PlayerEntry 는 API 응답용 플레이어 정보입니다.
type PlayerEntry struct {
	Name        string `json:"name"`
	UUID        string `json:"uuid"`
	Addr        string `json:"addr"`
	ConnectedAt string `json:"connected_at"`
}

// StatsInfo 는 API 응답용 통계 정보입니다.
type StatsInfo struct {
	ActiveSessions        int    `json:"active_sessions"`
	TotalPlayers          int32  `json:"total_players"`
	TotalBytesIn          int64  `json:"total_bytes_in"`          // cumulative all sessions
	TotalBytesOut         int64  `json:"total_bytes_out"`         // cumulative all sessions
	TotalBytesTransferred int64  `json:"total_bytes_transferred"` // cumulative in+out
	PortRangeStart        uint16 `json:"port_range_start"`
	PortRangeEnd          uint16 `json:"port_range_end"`
	UptimeSeconds         int64  `json:"uptime_seconds"`
}

var serverStartTime = time.Now()

// ──────────────────────────────────────────────
// API 핸들러
// ──────────────────────────────────────────────

// handleSessions 는 활성 세션 목록을 반환합니다.
func (ds *DashboardServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var sessions []SessionInfo
	ds.portTable.portToSession.Range(func(_, value interface{}) bool {
		sess := value.(*Session)
		if sess == portTaken {
			return true
		}

		var players []PlayerEntry
		sess.Players.Range(func(_, pv interface{}) bool {
			p := pv.(*PlayerInfo)
			players = append(players, PlayerEntry{
				Name:        p.Name,
				UUID:        p.UUID,
				Addr:        p.Addr,
				ConnectedAt: p.ConnectedAt.Format(time.RFC3339),
			})
			return true
		})
		if players == nil {
			players = []PlayerEntry{}
		}

		// Snapshot traffic for per-interval calculation
		curIn := sess.BytesIn.Load()
		curOut := sess.BytesOut.Load()
		lastIn := sess.LastBytesIn.Load()
		lastOut := sess.LastBytesOut.Load()
		sess.LastBytesIn.Store(curIn)
		sess.LastBytesOut.Store(curOut)

		sessions = append(sessions, SessionInfo{
			ClientID:         sess.ClientID,
			AssignedPort:     sess.AssignedPort,
			PlayerCount:      sess.PlayerCount.Load(),
			Players:          players,
			UnlimitedPlayers: sess.UnlimitedPlayers.Load(),
			BytesIn:          curIn,
			BytesOut:         curOut,
			IntervalIn:       curIn - lastIn,
			IntervalOut:      curOut - lastOut,
			CreatedAt:        sess.CreatedAt.Format(time.RFC3339),
			LastHeartbeat:    time.Unix(0, sess.LastHeartbeat.Load()).Format(time.RFC3339),
			Closed:           sess.Closed.Load(),
		})
		return true
	})

	if sessions == nil {
		sessions = []SessionInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

// handleStats 는 서버 통계를 반환합니다.
func (ds *DashboardServer) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var totalPlayers int32
	var totalBytesIn, totalBytesOut int64
	active := 0
	ds.portTable.portToSession.Range(func(_, value interface{}) bool {
		sess := value.(*Session)
		if sess == portTaken {
			return true
		}
		if !sess.Closed.Load() {
			active++
			totalPlayers += sess.PlayerCount.Load()
			totalBytesIn += sess.BytesIn.Load()
			totalBytesOut += sess.BytesOut.Load()
		}
		return true
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(StatsInfo{
		ActiveSessions:        active,
		TotalPlayers:          totalPlayers,
		TotalBytesIn:          totalBytesIn,
		TotalBytesOut:         totalBytesOut,
		TotalBytesTransferred: totalBytesIn + totalBytesOut,
		PortRangeStart:        ds.portTable.portRangeStart,
		PortRangeEnd:          ds.portTable.portRangeEnd,
		UptimeSeconds:         int64(time.Since(serverStartTime).Seconds()),
	})
}

// handleCloseSession 은 특정 세션을 강제 종료합니다.
func (ds *DashboardServer) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Port uint16 `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	sess, ok := ds.portTable.GetSession(req.Port)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	ds.portTable.removeSession(sess)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "closed"})
}

// handleToggleUnlimited 은 특정 세션의 인원 수 제한을 토글합니다.
func (ds *DashboardServer) handleToggleUnlimited(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Port uint16 `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	sess, ok := ds.portTable.GetSession(req.Port)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	var newVal bool
	for {
		old := sess.UnlimitedPlayers.Load()
		if sess.UnlimitedPlayers.CompareAndSwap(old, !old) {
			newVal = !old
			break
		}
	}

	log.Printf("[Dashboard] 인원 제한 토글: port=%d unlimited=%v", req.Port, newVal)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":            "ok",
		"unlimited_players": newVal,
	})
}
