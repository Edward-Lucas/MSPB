// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mspb/shared"
)

func RunCLI() {
	serverAddr := flag.String("server", shared.DefaultClientAddr, "중앙 서버 주소")
	localAddr := flag.String("local", shared.DefaultLocalMCAddr, "로컬 MC 서버 주소")
	token := flag.String("token", "", "인증 토큰 (선택)")
	flag.Parse()

	// 잠금 파일로 중복 실행 방지
	lockFile, err := acquireLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n\n", err)
		fmt.Println("아무 키나 누르면 종료합니다...")
		fmt.Scanln()
		os.Exit(1)
	}
	defer releaseLock(lockFile)

	cfg := &Config{
		ServerAddr:  *serverAddr,
		LocalMCAddr: *localAddr,
		Token:       *token,
	}

	client := New(cfg)

	// 대시보드 서버 시작 (localhost 전용)
	if err := client.dashboard.Start(shared.DefaultDashboardAddr); err != nil {
		log.Printf("[Client] 대시보드 시작 실패 (계속 진행): %v", err)
	}

	// 시그널 핸들링 — releaseLock 이 실행되도록 os.Exit 없이 종료
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("[Client] 종료 시그널 수신")
		client.Stop()
	}()

	fmt.Printf("MSPB Client  v1.0.0  2026-07-05\n")
	fmt.Println("────────────────────────────────────")
	fmt.Println("  official  https://mspb.r-e.kr")
	fmt.Println("  made by   MiFun")
	fmt.Println()
	client.Run()
}
