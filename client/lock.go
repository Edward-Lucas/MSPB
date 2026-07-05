// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFilePath 는 잠금 파일 경로입니다.
// 실행 파일과 같은 디렉토리에 .mspb-client.lock 으로 생성됩니다.
func lockFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		// 실행 파일 경로를 알 수 없으면 현재 디렉토리 사용
		return ".mspb-client.lock"
	}
	return filepath.Join(filepath.Dir(exe), ".mspb-client.lock")
}

// acquireLock 은 잠금 파일을 독점적으로 생성하여 잠급니다.
// 이미 잠금 파일이 존재하면 프로세스가 살아있는지 확인 후,
// 살아있다면 ErrAlreadyRunning 을 반환합니다.
// 성공 시 잠금 파일 핸들을 반환하며, 호출측은 반드시 defer releaseLock 해야 합니다.
func acquireLock() (*os.File, error) {
	path := lockFilePath()

	// 기존 잠금 파일 확인
	if existing, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644); err == nil {
		// 잠금 파일 생성 성공 — 이 프로세스가 첫 번째
		if _, err := fmt.Fprintf(existing, "%d", os.Getpid()); err != nil {
			existing.Close()
			os.Remove(path)
			return nil, fmt.Errorf("lock 파일 쓰기 실패: %w", err)
		}
		return existing, nil
	}

	// 잠금 파일이 이미 존재 — 프로세스가 살아있는지 확인
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("잠금 파일 읽기 실패: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		// 잠금 파일이 손상됨 — 강제 제거 후 재시도
		os.Remove(path)
		return acquireLock()
	}

	if isProcessAlive(pid) {
		return nil, fmt.Errorf("이미 실행 중인 MSPB Client가 있습니다 (PID: %d)", pid)
	}

	// 이전 프로세스가 종료됨 — 잠금 파일 교체
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("이전 잠금 파일 제거 실패: %w", err)
	}

	return acquireLock()
}

// releaseLock 은 잠금 파일을 닫고 제거합니다.
func releaseLock(f *os.File) {
	if f == nil {
		return
	}
	f.Close()
	os.Remove(lockFilePath())
}

const processQueryLimitedInformation = 0x1000

// isProcessAlive 은 주어진 PID의 프로세스가 현재 실행 중인지 확인합니다.
// Windows에서는 OpenProcess + WaitForSingleObject 방식으로 확인합니다.
func isProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(
		processQueryLimitedInformation|syscall.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)

	// WaitForSingleObject 로 프로세스 종료 여부 확인
	// 0(WAIT_OBJECT_0)이면 종료됨, TIMEOUT이면 아직 살아있음
	ret, _ := syscall.WaitForSingleObject(handle, 0)
	return ret == uint32(syscall.WAIT_TIMEOUT)
}
