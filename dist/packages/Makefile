# Build tarball and/or RPM package for various components

SHELL = /bin/sh
CWD := $(shell pwd)
RPMBUILDDIR := $(CWD)/build/rpmbuild

all: ovs-exporter.tar.gz

ovs-exporter.tar.gz: bld
	mkdir ovs-exporter/bin
	cp bin/ovn-kube-util ovs-exporter/bin
	cp bin/git_info ovs-exporter/bin
	tar -cf ovs-exporter.tar ovs-exporter
	gzip ovs-exporter.tar

.PHONY: rpm
rpm: ovs-exporter.tar.gz
	mkdir -p $(RPMBUILDDIR)/BUILD
	mkdir -p $(RPMBUILDDIR)/RPMS
	mkdir -p $(RPMBUILDDIR)/SOURCES
	mkdir -p $(RPMBUILDDIR)/SPECS
	cp ovs-exporter/ovs-exporter.spec $(RPMBUILDDIR)/SPECS/
	cp ovs-exporter.tar.gz $(RPMBUILDDIR)/SOURCES/
	rpmbuild --define '_topdir $(RPMBUILDDIR)' -bb $(RPMBUILDDIR)/SPECS/ovs-exporter.spec
	cp $(RPMBUILDDIR)/RPMS/*/ovs-exporter*rpm .
	rm -rf $(RPMBUILDDIR)

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
	rm -rf build bin ovs-exporter/bin ovs-exporter.tar.gz ovs-exporter-*.rpm
