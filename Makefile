
regen: mine golang

mine:
	go run . -left2right -noext fortio grol-io ldemailly > dependencies.dot
	dot -Tsvg dependencies.dot -o dependencies.svg; open dependencies.svg
	go run . -topo-sort -noext fortio grol-io ldemailly

golang:
	go run . -noext -left2right golang > dependencies_golang.dot
	dot -Tsvg dependencies_golang.dot -o dependencies_golang.svg; open dependencies_golang.svg
	dot -Tpng dependencies_golang.dot -o dependencies_golang.png; open dependencies_golang.png
	go run . -topo-sort -noext golang

import:
	go run ./aisplit
	git diff -w

export:
	go run ./aijoin *.go

.PHONY: regen mine golang import
