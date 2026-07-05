// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"encoding/json"
	"log"
	"math"
	"net"
	"net/http"
	"time"
)

// ──────────────────────────────────────────────
// 웹 서버 (외부 웹페이지용 — /api/stats 제공)
// ──────────────────────────────────────────────

// WebServer 는 외부 웹페이지에 stats API를 제공하는 경량 서버입니다.
type WebServer struct {
	portTable    *PortTable
	listener     net.Listener
	lastPollTime time.Time
}

// NewWebServer 는 웹 서버를 생성합니다.
func NewWebServer(pt *PortTable) *WebServer {
	return &WebServer{
		portTable:    pt,
		lastPollTime: time.Now(),
	}
}

// Start 는 웹 서버를 시작합니다.
func (ws *WebServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", ws.handleStats)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ws.listener = ln

	log.Printf("[Web] 웹 서버 시작: http://%s", addr)

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[Web] 서버 에러: %v", err)
		}
	}()

	return nil
}

// handleStats 는 서버 통계를 반환합니다.
func (ws *WebServer) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var totalBytesIn, totalBytesOut int64
	var totalIntervalIn, totalIntervalOut int64
	active := 0
	ws.portTable.portToSession.Range(func(_, value interface{}) bool {
		sess := value.(*Session)
		if sess == portTaken {
			return true
		}
		if !sess.Closed.Load() {
			active++
			curIn := sess.BytesIn.Load()
			curOut := sess.BytesOut.Load()
			lastIn := sess.LastBytesIn.Load()
			lastOut := sess.LastBytesOut.Load()
			sess.LastBytesIn.Store(curIn)
			sess.LastBytesOut.Store(curOut)
			totalBytesIn += curIn
			totalBytesOut += curOut
			totalIntervalIn += curIn - lastIn
			totalIntervalOut += curOut - lastOut
		}
		return true
	})

	totalInterval := totalIntervalIn + totalIntervalOut
	// 초당 전송량 기준: 100MB/s = 100%
	elapsed := time.Since(ws.lastPollTime).Seconds()
	ws.lastPollTime = time.Now()
	netPct := 0.0
	if totalInterval > 0 && elapsed > 0 {
		bytesPerSec := float64(totalInterval) / elapsed
		pct := bytesPerSec / (100 * 1024 * 1024) * 100
		if pct > 0 && pct < 1 {
			pct = 1
		}
		if pct > 100 {
			pct = 100
		}
		netPct = math.Ceil(pct)
	}

	totalPorts := int(ws.portTable.portRangeEnd - ws.portTable.portRangeStart)
	portPct := 0.0
	if totalPorts > 0 {
		pct := float64(active) / float64(totalPorts) * 100
		if pct > 0 && pct < 1 {
			pct = 1
		}
		portPct = math.Ceil(pct)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"network_load": netPct,
		"port_load":    portPct,
	})
}
