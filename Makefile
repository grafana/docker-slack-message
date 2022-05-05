DOCKER_REGISTRY ?= "grafana"
DOCKER_REPO ?= $(DOCKER_REGISTRY)/docker-slack-message

LATEST_IMAGE := $(DOCKER_REPO):latest
COMMIT_IMAGE := $(DOCKER_REPO):$(shell git rev-parse --short HEAD)
DATE_IMAGE := $(COMMIT_IMAGE)-$(shell date +%Y-%m-%d)


.PHONY: build
build:
	docker build -t $(LATEST_IMAGE) -t $(COMMIT_IMAGE) -t $(DATE_IMAGE) .

.PHONY: docker-push
push: build
	docker push $(LATEST_IMAGE)
	docker push $(COMMIT_IMAGE)
	docker push $(DATE_IMAGE)

