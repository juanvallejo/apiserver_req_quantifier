all:
	mkdir -p ./bin && go build -o bin/quant cmd/quant.go
