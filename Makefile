VERSION=$(shell git describe --tags | sed 's/^v//')
BUILD=$(shell date +%FT%T%z)
HOST=$(shell hostname)
NAME=$(shell basename `pwd`)

all:	image
	

image:	compile
	docker build \
	-t registry.scw.systems/$(NAME):$(VERSION) \
	-t registry.scw.systems/$(NAME):latest \
	.

release:
	docker push registry.scw.systems/$(NAME):latest
	docker push registry.scw.systems/$(NAME):$(VERSION)

compile:
	docker run --rm -v $(shell pwd):/src/$(NAME) \
	-e VERSION=$(VERSION) -e BUILD=$(BUILD) -e HOST=$(HOST) \
	-w /src/$(NAME) \
	golang:alpine ./build.sh
