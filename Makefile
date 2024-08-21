.PHONY: build
build:
	docker buildx build --platform linux/amd64,linux/arm64 --push --tag ghcr.io/sj14/site2rss/site2rss:latest .
