INSTALL_DIR ?= $(HOME)/.local/bin

build:
	go build -o ap-query .

install:
	go build -o $(INSTALL_DIR)/ap-query .
