# Build tarball package for various components

all: ovs-exporter.tar.gz

ovs-exporter.tar.gz: bld
	mkdir ovs-exporter/bin
	cp bin/ovn-kube-util ovs-exporter/bin
	cp bin/git_info ovs-exporter/bin
	tar -cf ovs-exporter.tar ovs-exporter
	gzip ovs-exporter.tar

.PHONY: ../../go-controller/_output/go/bin/ovnkube
../../go-controller/_output/go/bin/ovnkube:
	cd ../../go-controller ; make

BRANCH = $(shell git rev-parse  --symbolic-full-name HEAD)
COMMIT = $(shell git rev-parse  HEAD)
bld: ../../go-controller/_output/go/bin/ovnkube
	cp -r ../../go-controller/_output/go/bin .
	echo "ref: ${BRANCH}  commit: ${COMMIT}" > bin/git_info

.PHONY: clean
clean:
	rm -rf bin ovs-exporter/bin ovs-exporter.tar.gz
