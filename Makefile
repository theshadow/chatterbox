.PHONY: all deps generate
all: deps generate

deps:
	go mod vendor

generate:
	protoc --go_out=plugins=grpc:. chatterbox.proto