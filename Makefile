build:
	go build ./testgrid/cmd/build-indexer
	go build ./cmd/search
.PHONY: build