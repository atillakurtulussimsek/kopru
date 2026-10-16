BIN := ../dist/kopru

all:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o $(BIN)-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BIN)-amd64 .
	lipo -create -output $(BIN) $(BIN)-arm64 $(BIN)-amd64 && rm $(BIN)-arm64 $(BIN)-amd64

test:
	go test ./...
