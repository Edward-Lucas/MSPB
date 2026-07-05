// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package server

import (
	"fmt"
	"sync"
	"testing"
)

func TestPortTableAllocate(t *testing.T) {
	pt := NewPortTable(30000, 30010) // 좁은 범위로 테스트

	// 단일 할당
	port, err := pt.AllocatePort("client-1")
	if err != nil {
		t.Fatalf("AllocatePort: %v", err)
	}
	if port < 30000 || port >= 30010 {
		t.Errorf("port %d out of range [30000, 30010)", port)
	}

	// 중복 할당 시 같은 포트 반환
	port2, err := pt.AllocatePort("client-1")
	if err != nil {
		t.Fatalf("AllocatePort (repeat): %v", err)
	}
	if port != port2 {
		t.Errorf("repeat allocate: got %d, want %d", port2, port)
	}

	// 다른 클라이언트 할당
	port3, err := pt.AllocatePort("client-2")
	if err != nil {
		t.Fatalf("AllocatePort client-2: %v", err)
	}
	if port3 == port {
		t.Error("different clients should get different ports")
	}

	// 세션 조회
	sess, ok := pt.GetSession(port)
	if !ok {
		t.Fatal("GetSession failed")
	}
	if sess.ClientID != "client-1" {
		t.Errorf("ClientID: got %q, want %q", sess.ClientID, "client-1")
	}

	// 클라이언트 ID로 조회
	sess2, ok := pt.GetSessionByClient("client-2")
	if !ok {
		t.Fatal("GetSessionByClient failed")
	}
	if sess2.AssignedPort != port3 {
		t.Errorf("port: got %d, want %d", sess2.AssignedPort, port3)
	}

	// 정리
	sess.Close()
	sess2.Close()
}

func TestPortTableConcurrent(t *testing.T) {
	pt := NewPortTable(40000, 40100) // 넉넉한 범위

	const N = 30
	var wg sync.WaitGroup
	results := make([]uint16, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			clientID := fmt.Sprintf("concurrent-%d", id)
			port, err := pt.AllocatePort(clientID)
			results[id] = port
			errs[id] = err
		}(i)
	}

	wg.Wait()

	// 모든 할당이 성공해야 함
	for i, err := range errs {
		if err != nil {
			t.Errorf("client %d: %v", i, err)
		}
	}

	// 모든 포트가 고유해야 함
	seen := make(map[uint16]int)
	for i, port := range results {
		if port == 0 {
			continue
		}
		if prev, exists := seen[port]; exists {
			t.Errorf("duplicate port %d: client %d and client %d", port, prev, i)
		}
		seen[port] = i
	}

	// 정리
	pt.portToSession.Range(func(_, value interface{}) bool {
		value.(*Session).Close()
		return true
	})
}

func TestPortTableExhaustion(t *testing.T) {
	pt := NewPortTable(50000, 50003) // 3개 포트만

	for i := 0; i < 3; i++ {
		clientID := fmt.Sprintf("exhaust-client-%d", i)
		_, err := pt.AllocatePort(clientID)
		if err != nil {
			t.Fatalf("AllocatePort %d: %v", i, err)
		}
	}

	// 4번째는 실패해야 함 (모든 포트 소진)
	_, err := pt.AllocatePort("exhaust-client-overflow")
	if err == nil {
		t.Error("expected port exhaustion error")
	}

	// 정리
	pt.portToSession.Range(func(_, value interface{}) bool {
		value.(*Session).Close()
		return true
	})
}
