VERSION ?= 0.1.0
PREFIX  ?= $(HOME)/.local
LDFLAGS := -s -w -X dbmeta-cli/internal/cli.Version=$(VERSION)

.PHONY: build test install uninstall dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/dbmeta-cli ./cmd/dbmeta-cli

test:
	go vet ./...
	go test ./...

# 安装二进制到 $(PREFIX)/bin,并安装全局 skill(~/.agents/skills/dbmeta)。
install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/dbmeta-cli $(PREFIX)/bin/dbmeta-cli
	$(PREFIX)/bin/dbmeta-cli skill install
	@echo
	@echo "已安装到 $(PREFIX)/bin/dbmeta-cli"
	@echo "$$PATH" | tr ':' '\n' | grep -qx "$(PREFIX)/bin" || echo "提示: 请把 $(PREFIX)/bin 加入 PATH"
	@echo "下一步: 在项目目录运行 dbmeta-cli init,再用 dbmeta-cli instance add 登记实例"

uninstall:
	-$(PREFIX)/bin/dbmeta-cli skill uninstall
	rm -f $(PREFIX)/bin/dbmeta-cli

# 交叉编译,产物在 dist/
dist:
	rm -rf dist && mkdir -p dist
	for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ $$os = windows ] && ext=".exe"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
			-o dist/dbmeta-cli-$(VERSION)-$$os-$$arch$$ext ./cmd/dbmeta-cli || exit 1; \
	done
	ls -l dist

clean:
	rm -rf bin dist
