REGISTRY=registry.cn-hangzhou.aliyuncs.com/louhwz
ARCHS?=amd64
DEV_TAG?=dev
RELEASE_TAG?=release

.PHONY: build-dev-images release

build-dev-images:
	@for arch in ${ARCHS} ; do \
    	docker build -t ${REGISTRY}/ramatest:${DEV_TAG}-$$arch -f Dockerfile.$$arch ./; \
    done

release:
	@for arch in ${ARCHS} ; do \
		docker build -t ${REGISTRY}/ramatest:${RELEASE_TAG}-$$arch -f Dockerfile.$$arch ./; \
	done

code-gen:
	cd hack && chmod u+x ./update-codegen.sh && ./update-codegen.sh

push: build-dev-images
	docker push ${REGISTRY}/ramatest:${DEV_TAG}-amd64