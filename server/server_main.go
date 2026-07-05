// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mspb/shared"
)

// Run 은 중앙 서버를 시작합니다.
func Run(addr string) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	publicIP := shared.ServerPublicIP

	log.Printf("═══════════════════════════════════════════════")
	log.Printf("  MSPB Central Server")
	log.Printf("  바인딩: %s", addr)
	log.Printf("  공인 IP: %s", publicIP)
	log.Printf("  외부 포트 범위: %d-%d", shared.PublicPortRangeStart, shared.PublicPortRangeEnd)
	log.Printf("═══════════════════════════════════════════════")

	portTable := NewPortTable(shared.PublicPortRangeStart, shared.PublicPortRangeEnd)
	portTable.onChange = func(active int) {
		log.Printf("[Server] 활성 세션: %d", active)
	}
	tunnelMgr := NewTunnelManager(portTable, publicIP)

	// 대시보드 서버 시작 (localhost 전용 — 127.0.0.1에만 바인딩)
	dashboard := NewDashboardServer(portTable, "")
	if err := dashboard.Start(shared.DefaultDashboardAddr); err != nil {
		log.Printf("[Server] 대시보드 서버 시작 실패 (계속 진행): %v", err)
	}

	// 웹 서버 시작 (외부 웹페이지용)
	web := NewWebServer(portTable)
	if err := web.Start(shared.DefaultWebAddr); err != nil {
		log.Printf("[Server] 웹 서버 시작 실패 (계속 진행): %v", err)
	}

	// 제어 리스너 시작
	controlListener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[Server] 제어 리스너 시작 실패: %v", err)
	}
	defer controlListener.Close()

	log.Printf("[Server] 제어 리스너 시작: %s", addr)

	// 하트비트 회수 고루틴 (30초마다 stale 세션 정리)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			reclaimed := portTable.ReclaimStaleSessions()
			if reclaimed > 0 {
				log.Printf("[Server] stale 세션 회수: %d개", reclaimed)
			}
		}
	}()

	// 시그널 핸들링
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("[Server] 종료 시그널 수신, 정리 중...")
		controlListener.Close()
		os.Exit(0)
	}()

	// 메인 수신 루프
	for {
		conn, err := controlListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("[Server] 리스너 종료: %v", err)
				return
			}
			log.Printf("[Server] 수신 에러 (계속 진행): %v", err)
			continue
		}

		go tunnelMgr.HandleClientConnection(conn)
	}
}

// RunWithDefaults 는 기본 설정으로 서버를 시작합니다.
func RunWithDefaults() {
	Run(shared.DefaultControlAddr)
}
