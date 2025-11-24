TEMP_PATH :=  $(CURDIR)/tmp

.PHONY: poetry-install
poetry-install:
	poetry install --no-root

.PHONY: build
build: poetry-install
	poetry run mkdocs build

.PHONY: serve
serve: poetry-install
	poetry run mkdocs serve

.PHONY: brew
brew:
	brew bundle --force
