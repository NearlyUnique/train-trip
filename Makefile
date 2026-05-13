BINARY      := build/train_trip
BINARY_ARM  := build/train_trip_arm6
PI_HOST     := pi
PI_DIR      := /home/pi/train_trip
PORT        := 8083

.PHONY: build build-arm run clean deploy test

# Build for local architecture
build:
	go build -o $(BINARY) .

# Build for ARM6 (Raspberry Pi Zero / Pi 1)
build-arm:
	GOOS=linux GOARCH=arm GOARM=6 go build -o $(BINARY_ARM) .

# build and run locally
run: build test
	PORT=$(PORT) ./$(BINARY)

# clean deployment artifacts
clean:
	rm -f $(BINARY) $(BINARY_ARM)

## Run all tests
test:
	go test ./...

## Deploy to Pi: build ARM6 binary, copy files (preserving .env), restart service
deploy: build-arm test
	ssh $(PI_HOST) "sudo systemctl stop train_trip || true"
	ssh $(PI_HOST) "mkdir -p $(PI_DIR) && rm -f $(PI_DIR)/train_trip && rm -rf $(PI_DIR)/linux $(PI_DIR)/templates $(PI_DIR)/static"
	scp $(BINARY_ARM)  $(PI_HOST):$(PI_DIR)/train_trip
	scp .env           $(PI_HOST):$(PI_DIR)/.env
	scp -r linux/      $(PI_HOST):$(PI_DIR)/
	scp -r templates/  $(PI_HOST):$(PI_DIR)/
	scp -r static/     $(PI_HOST):$(PI_DIR)/
	ssh $(PI_HOST) "bash $(PI_DIR)/linux/run.sh"