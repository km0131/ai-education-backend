#!/bin/bash
set -e
VERSION=$(date +%Y%m%d)
docker build --load -t sandbox-base:${VERSION} -t sandbox-base:latest .
