# ann2html
# See LICENSE file for copyright and license details.
#
# The GUI uses raylib through cgo, so every target needs a C compiler for the
# target platform. Build each target on its own OS, or point CC at a cross
# compiler (e.g. CC=x86_64-w64-mingw32-gcc for windows-x86_64 on Linux).

.PHONY: build test dist linux-x86_64 windows-x86_64 macos-x86_64 macos-arm64 clean-releases

GOOPTIONS = CGO_ENABLED=1
LDFLAGS = -ldflags="-w -s -buildid=" -trimpath -o
WINLDFLAGS = -ldflags="-w -s -buildid= -H windowsgui" -trimpath -o

build:
	go build -o build/ .

test:
	go test ./...

all: clean-releases dist

dist: linux-x86_64 windows-x86_64 macos-x86_64 macos-arm64

linux-x86_64:
	${GOOPTIONS} GOOS=linux GOARCH=amd64 go build ${LDFLAGS} release/$@/ann2html/ann2html
	cp release-includes/README-linux release/$@/ann2html/README
	cp release-includes/ann release/$@/ann2html/ann
	tar --owner=0 --group=0 --mode='og-w' -czvf release/ann2html-$@.tar.gz -C release/$@ .
	rm -r release/$@

windows-x86_64:
	${GOOPTIONS} GOOS=windows GOARCH=amd64 go build ${WINLDFLAGS} release/$@/ann2html/ann2html.exe
	cp release-includes/README-windows release/$@/ann2html/README.txt
	(cd release/$@ && zip -9 -y -r -X - ann2html/ > ../ann2html-$@.zip)
	rm -r release/$@

macos-x86_64:
	${GOOPTIONS} GOOS=darwin GOARCH=amd64 go build ${LDFLAGS} release/$@/ann2html/ann2html
	cp release-includes/README-macos release/$@/ann2html/README
	(cd release/$@ && zip -9 -y -r -X - ann2html/ > ../ann2html-$@.zip)
	rm -r release/$@

macos-arm64:
	${GOOPTIONS} GOOS=darwin GOARCH=arm64 go build ${LDFLAGS} release/$@/ann2html/ann2html
	cp release-includes/README-macos release/$@/ann2html/README
	(cd release/$@ && zip -9 -y -r -X - ann2html/ > ../ann2html-$@.zip)
	rm -r release/$@

clean-releases:
	rm -f release/*
