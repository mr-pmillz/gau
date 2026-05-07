#!/usr/bin/env bash

if [[ -n "$CI" ]]; then
    exec /bin/bash
else
    exec "$@"
fi
