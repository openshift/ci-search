build:
	go build -mod vendor ./cmd/search
.PHONY: build

bindata:
	go-bindata -fs -pkg bindata -o pkg/bindata/bindata.go -prefix "static/" static/
.PHONY: bindata
