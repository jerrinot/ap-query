INSTALL_DIR ?= $(HOME)/.local/bin

build:
	go build -o ap-query .

install:
	go build -o $(INSTALL_DIR)/ap-query .

release:
	@test "$$(git branch --show-current)" = "master" || { echo "error: not on master"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "error: uncommitted changes"; exit 1; }
	$(eval LATEST := $(shell git tag --sort=-v:refname | head -1))
	$(eval NEXT := $(shell echo $(LATEST) | awk -F. '{print $$1"."$$2+1}'))
	@echo "Latest: $(LATEST)  Next: $(NEXT)"
	@read -p "Press Enter to tag and push, Ctrl-C to abort. " _
	git tag $(NEXT)
	git push origin master $(NEXT)
