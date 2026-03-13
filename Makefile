GOCMD=go
TEMPL=templ
TAILWIND=tailwindcss
BUILD_DIR=./tmp
BINARY_NAME=gocms
ADMIN_BINARY_NAME=gocms-admin

GOCMS_PATH=./cmd/$(BINARY_NAME)
GOCMS_ADMIN_PATH=./cmd/$(ADMIN_BINARY_NAME)

all: build test


build: tailwindcss
	$(TEMPL) generate
	$(GOCMD) build -v -o $(BUILD_DIR)/$(BINARY_NAME) $(GOCMS_PATH)
	$(GOCMD) build -v -o $(BUILD_DIR)/$(ADMIN_BINARY_NAME) $(GOCMS_ADMIN_PATH)

test: 
	$(GOCMD) test -v ./...

tailwindcss:
	$(TAILWIND) -i ./static/style.css -o ./static/css/style.css -m 

clean:
	$(GOCMD) clean
	rm -rf $(BUILD_DIR)/$(BINARY_NAME)
swagger-generate:
	swag init -g cmd/gocms-admin/main.go -o docs

install-tools:
	go install github.com/pressly/goose/v3/cmd/goose@v3.18.0
	go install github.com/a-h/templ/cmd/templ@v0.3.898
	go install github.com/cosmtrek/air@v1.49.0 
	go install github.com/swaggo/swag/cmd/swag@latest


.PHONY: all build test clean
		