#!/bin/bash

git pull

go version
go build -trimpath -o bin/metatrash ./cmd/metatrash
./bin/metatrash version
go test ./internal/service -run '^TestMCPSmoke$' -count=1
read -p "BUILD COMPLETE, press enter to install"

sudo cp -a /usr/local/bin/metatrash /usr/local/bin/metatrash.backup

sudo systemctl stop metatrash
sudo install -o root -g root -m 0755 bin/metatrash /usr/local/bin/metatrash
sudo systemctl start metatrash
sudo systemctl status metatrash --no-pager

sleep 2
curl -fsS http://127.0.0.1:8080/healthz
