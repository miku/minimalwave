SHELL = /bin/bash

minimalwave: minimalwave.go
	go build -o minimalwave minimalwave.go

.PHONY: clean
clean:
	rm -f minimalwave
