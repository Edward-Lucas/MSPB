// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package shared

import "time"

// ──────────────────────────────────────────────
// 공통 설정 상수
// ──────────────────────────────────────────────

const (
	// 서버 설정
	DefaultControlAddr   = "0.0.0.0:19132"     // 중앙 서버 바인딩 주소
	DefaultClientAddr    = "mspb.r-e.kr:19132" // 클라이언트 기본 연결 주소
	ServerPublicIP       = "mspb.r-e.kr"       // 서버 공인 IP
	PublicPortRangeStart = 25500               // 외부 할당 포트 범위 시작
	PublicPortRangeEnd   = 25600               // 외부 할당 포트 범위 종료 (exclusive)
	DefaultDashboardAddr = "127.0.0.1:18080"   // 대시보드 서버 주소 (서버/클라이언트 공용)
	DefaultWebAddr       = "127.0.0.1:18081"   // 웹 서버 주소 (외부 웹페이지용)

	// 클라이언트 설정
	DefaultLocalMCAddr = "127.0.0.1:25565" // 로컬 MC 서버 기본 주소

	// yamux 설정
	YamuxStreamOpenTimeout  = 10 * time.Second
	YamuxStreamCloseTimeout = 5 * time.Second
	YamuxKeepAliveInterval  = 10 * time.Second
	YamuxMaxStreamCount     = 256

	// 패킷 peek 설정
	PeekTimeoutSeconds = 10   // 첫 패킷 대기 타임아웃 (초)
	PeekBufferSize     = 1024 // peek 버퍼 크기

	// 플레이어 제한
	MaxPlayersPerClient = 8 // 클라이언트(호스트)당 최대 동시 접속 인원
)
