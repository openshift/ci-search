build:
	go build -mod vendor ./testgrid/cmd/build-indexer
	go build -mod vendor ./cmd/search
.PHONY: build