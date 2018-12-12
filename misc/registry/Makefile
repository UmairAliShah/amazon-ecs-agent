BUILD_PLATFORM:=$(shell uname -m)

.PHONY: all

ifeq (${BUILD_PLATFORM},aarch64)
all:
	docker build -t test-ecs-registry/registry:make -f Dockerfile.arm .
else
all:
	docker build -t test-ecs-registry/registry:make -f Dockerfile .
endif