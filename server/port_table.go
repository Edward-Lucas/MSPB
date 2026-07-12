// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mspb/shared"
)

// ──────────────────────────────────────────────
// 포트 할당 테이블 (Concurrency-Safe)
// ──────────────────────────────────────────────

// PlayerInfo 는 접속 중인 플레이어 정보입니다.
type PlayerInfo struct {
	Name        string
	UUID        string
	Addr        string // 외부 접속 IP
	ConnectedAt time.Time
}

// Session 은 하나의 활성 터널 세션을 나타냅니다.
type Session struct {
	ClientID         string
	AssignedPort     uint16
	Listener         net.Listener // 외부 리스너
	DataSession      *yamux.Session
	CreatedAt        time.Time
	LastHeartbeat    atomic.Int64 // UnixNano
	PlayerCount      atomic.Int32 // 현재 접속 중인 플레이어 수
	Closed           atomic.Bool
	UnlimitedPlayers atomic.Bool // true이면 인원 수 제한 해제
	mu               sync.Mutex

	// Player tracking
	Players sync.Map // playerName(string) -> *PlayerInfo

	// Traffic counters (cumulative)
	BytesIn  atomic.Int64 // 외부 → 터널 (incoming)
	BytesOut atomic.Int64 // 터널 → 외부 (outgoing)

	// 실시간 전송량 (백그라운드 티커가 1초마다 갱신)
	RateIn  atomic.Int64 // bytes/sec in
	RateOut atomic.Int64 // bytes/sec out

	// ticker 내부 스냅샷
	lastBytesIn  int64
	lastBytesOut int64
	tickerDone   chan struct{} // 세션 종료 시 닫힘
}

// StartRateTicker 는 1초마다 바이트 카운터를 샘플링하여
// RateIn/RateOut를 갱신하는 백그라운드 고루틴을 시작합니다.
func (s *Session) StartRateTicker() {
	s.tickerDone = make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				curIn := s.BytesIn.Load()
				curOut := s.BytesOut.Load()
				s.RateIn.Store(curIn - s.lastBytesIn)
				s.RateOut.Store(curOut - s.lastBytesOut)
				s.lastBytesIn = curIn
				s.lastBytesOut = curOut
			case <-s.tickerDone:
				return
			}
		}
	}()
}

// IncrementPlayer 은 현재 플레이어 수를 1 증가시킵니다.
// UnlimitedPlayers가 true이면 인원 수 제한을 적용하지 않습니다.
// 최대 인원 초과 시 false를 반환합니다.
func (s *Session) IncrementPlayer(max int) bool {
	for {
		cur := s.PlayerCount.Load()
		if !s.UnlimitedPlayers.Load() && int(cur) >= max {
			return false
		}
		if s.PlayerCount.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// DecrementPlayer 은 현재 플레이어 수를 1 감소시킵니다.
func (s *Session) DecrementPlayer() {
	for {
		cur := s.PlayerCount.Load()
		if cur <= 0 {
			return
		}
		if s.PlayerCount.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// AddPlayer registers a connected player.
func (s *Session) AddPlayer(name, uuid, addr string) {
	s.Players.Store(name, &PlayerInfo{
		Name:        name,
		UUID:        uuid,
		Addr:        addr,
		ConnectedAt: time.Now(),
	})
}

// RemovePlayer removes a disconnected player.
func (s *Session) RemovePlayer(name string) {
	s.Players.Delete(name)
}

// Close 는 세션의 리스너를 정리합니다.
func (s *Session) Close() {
	if s.Closed.CompareAndSwap(false, true) {
		if s.tickerDone != nil {
			close(s.tickerDone)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.Listener != nil {
			s.Listener.Close()
		}
		if s.DataSession != nil {
			s.DataSession.Close()
		}
		log.Printf("[PortTable] 세션 종료: client=%s port=%d", s.ClientID, s.AssignedPort)
	}
}

// portTaken 은 포트 사용 중임을 표시하는 sentinel입니다.
// AllocatePort에서 리스너 바인딩 전에 portToSession에 먼저 등록하여
// 동시 할당 시 포트 충돌을 방지합니다.
var portTaken = &Session{}

// PortTable 은 포트 할당 상태를 관리하는 동시성 안전 테이블입니다.
//
// 내부적으로 sync.Map을 사용하여 읽기 중심의 워크로드에서
// lock-free 성능을 확보합니다.
type PortTable struct {
	// portToSession: port(uint16) -> *Session
	portToSession sync.Map
	// clientToSession: clientID(string) -> *Session
	clientToSession sync.Map

	portRangeStart uint16
	portRangeEnd   uint16

	// 세션 수 추적
	activeCount atomic.Int64 // 현재 활성 세션 수

	// 세션 변동 콜백 — 세션이 추가/제거될 때마다 호출됨
	onChange func(active int)
}

// NewPortTable 은 새로운 포트 테이블을 생성합니다.
func NewPortTable(start, end uint16) *PortTable {
	return &PortTable{
		portRangeStart: start,
		portRangeEnd:   end,
	}
}

// AllocatePort 은 사용 가능한 랜덤 포트를 할당합니다.
// 동시 호출 시 CAS 스타일의 원자적 연산으로 충돌을 방지합니다.
func (pt *PortTable) AllocatePort(clientID string) (uint16, error) {
	// 이미 이 클라이언트에 할당된 세션이 있는지 확인
	if existing, ok := pt.clientToSession.Load(clientID); ok {
		sess := existing.(*Session)
		if !sess.Closed.Load() {
			return sess.AssignedPort, nil
		}
		// 닫힌 세션은 정리
		pt.removeSession(sess)
	}

	portRange := int(pt.portRangeEnd - pt.portRangeStart)
	if portRange <= 0 {
		return 0, fmt.Errorf("invalid port range: %d-%d", pt.portRangeStart, pt.portRangeEnd)
	}

	// 랜덤 셔플 순서로 포트 시도 (충돌 시 빠른 탐색)
	offsets := rand.Perm(portRange)

	for _, offset := range offsets {
		port := pt.portRangeStart + uint16(offset)

		// CAS: portToSession에 sentinel을 먼저 등록하여 포트 선점
		if _, loaded := pt.portToSession.LoadOrStore(port, portTaken); loaded {
			continue // 이미 사용 중 — 다음 포트
		}

		// 리스너 바인딩 시도
		listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			pt.portToSession.Delete(port) // 바인딩 실패 시 롤백
			continue
		}

		// 세션 생성 및 등록
		sess := &Session{
			ClientID:     clientID,
			AssignedPort: port,
			Listener:     listener,
			CreatedAt:    time.Now(),
		}
		sess.LastHeartbeat.Store(time.Now().UnixNano())

		pt.portToSession.Store(port, sess)
		pt.clientToSession.Store(clientID, sess)
		sess.StartRateTicker()

		log.Printf("[PortTable] 포트 할당: client=%s port=%d", clientID, port)
		pt.notifyChange()
		return port, nil
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", pt.portRangeStart, pt.portRangeEnd)
}

// GetSession 은 포트 번호로 세션을 조회합니다.
// sentinel인 경우 nil을 반환합니다.
func (pt *PortTable) GetSession(port uint16) (*Session, bool) {
	v, ok := pt.portToSession.Load(port)
	if !ok {
		return nil, false
	}
	sess := v.(*Session)
	if sess == portTaken {
		return nil, false
	}
	return sess, true
}

// GetSessionByClient 는 클라이언트 ID로 세션을 조회합니다.
func (pt *PortTable) GetSessionByClient(clientID string) (*Session, bool) {
	v, ok := pt.clientToSession.Load(clientID)
	if !ok {
		return nil, false
	}
	return v.(*Session), true
}

// UpdateHeartbeat 은 세션의 하트비트 시간을 갱신합니다.
func (pt *PortTable) UpdateHeartbeat(port uint16) {
	if sess, ok := pt.GetSession(port); ok {
		sess.LastHeartbeat.Store(time.Now().UnixNano())
	}
}

// removeSession 은 테이블에서 세션을 제거합니다.
func (pt *PortTable) removeSession(sess *Session) {
	pt.portToSession.Delete(sess.AssignedPort)
	pt.clientToSession.Delete(sess.ClientID)
	sess.Close()
	pt.notifyChange()
}

// ReclaimStaleSessions 은 하트비트 타임아웃된 세션을 회수합니다.
func (pt *PortTable) ReclaimStaleSessions() int {
	now := time.Now().UnixNano()
	reclaimed := 0

	pt.portToSession.Range(func(_, value interface{}) bool {
		sess := value.(*Session)
		lastBeat := sess.LastHeartbeat.Load()
		if now-lastBeat > int64(shared.HeartbeatTimeout) {
			log.Printf("[PortTable] 하트비트 타임아웃 회수: client=%s port=%d", sess.ClientID, sess.AssignedPort)
			pt.removeSession(sess)
			reclaimed++
		}
		return true
	})

	return reclaimed
}

// notifyChange 는 현재 활성 세션 수를 계산하고 콜백을 호출합니다.
func (pt *PortTable) notifyChange() {
	if pt.onChange != nil {
		pt.onChange(pt.Stats())
	}
}

// Stats 는 현재 포트 테이블 상태를 반환합니다.
func (pt *PortTable) Stats() (active int) {
	pt.portToSession.Range(func(_, _ interface{}) bool {
		active++
		return true
	})
	return
}
