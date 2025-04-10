
regen: mine golang with-ext

mine:
	go run . -left2right -noext fortio grol-io ldemailly > dependencies.dot
	dot -Tsvg dependencies.dot -o dependencies.svg; open dependencies.svg
	go run . -topo-sort -noext fortio grol-io ldemailly > dependencies_sorted.txt

golang:
	go run . -noext -left2right golang > dependencies_golang.dot
	dot -Tsvg dependencies_golang.dot -o dependencies_golang.svg; open dependencies_golang.svg
	# dot -Tpng dependencies_golang.dot -o dependencies_golang.png; open dependencies_golang.png
	go run . -topo-sort -noext golang > dependencies_golang_sorted.txt

with-ext:
	go run . -left2right fortio grol-io ldemailly > dependencies_with_ext.dot
	dot -Tsvg dependencies_with_ext.dot -o dependencies_with_ext.svg; open dependencies_with_ext.svg
	go run . -topo-sort fortio grol-io ldemailly > dependencies_with_ext_sorted.txt

import:
	go run ./aisplit
	git diff -w

export:
	go run ./aijoin *.go README.md dependencies_golang.dot

.PHONY: regen mine golang import export with-ext
