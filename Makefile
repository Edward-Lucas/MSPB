.PHONY: all clean server client resource sign

VERSION    ?= 1.0.0
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)

CERT_FILE ?= certificate.pfx
CERT_PASS ?=

all: resource server client

# 리소스 파일 생성 (아이콘 + 매니페스트 + 버전 정보)
resource: resources/icon.png
	go-winres simply --arch amd64 \
		--out cmd/server/rsrc \
		--manifest cli \
		--product-version $(VERSION) \
		--file-version $(VERSION) \
		--product-name "MSPB Server" \
		--file-description "MSPB - Minecraft Server Port Bridge (Central Server)" \
		--copyright "Copyright (C) 2026 MSPB" \
		--original-filename mspb-server.exe \
		--icon resources/icon.png
	go-winres simply --arch amd64 \
		--out cmd/client/rsrc \
		--manifest cli \
		--product-version $(VERSION) \
		--file-version $(VERSION) \
		--product-name "MSPB Client" \
		--file-description "MSPB - Minecraft Server Port Bridge (Host Client)" \
		--copyright "Copyright (C) 2026 MSPB" \
		--original-filename mspb-client.exe \
		--icon resources/icon.png

# 서버 빌드
server:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o mspb-server.exe ./cmd/server

# 클라이언트 빌드
client:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o mspb-client.exe ./cmd/client

# 테스트
test:
	go test ./...

# 정리
clean:
	rm -f mspb-server.exe mspb-client.exe
	rm -f cmd/server/rsrc_*.syso
	rm -f cmd/client/rsrc_*.syso

# 디지털 서명 (signtool 사용 - Windows SDK 필요)
sign:
	@echo "=== mspb-server.exe 서명 ==="
	signtool sign /f $(CERT_FILE) /p $(CERT_PASS) /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 mspb-server.exe
	@echo "=== mspb-client.exe 서명 ==="
	signtool sign /f $(CERT_FILE) /p $(CERT_PASS) /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 mspb-client.exe

# 서명 검증
verify:
	signtool verify /pa /v mspb-server.exe
	signtool verify /pa /v mspb-client.exe
