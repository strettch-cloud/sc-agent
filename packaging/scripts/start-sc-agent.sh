#!/bin/sh

AGENT_BINARY="/usr/local/bin/sc-agent"

if [ ! -f "$AGENT_BINARY" ]; then
    echo "ERROR: Agent binary $AGENT_BINARY not found!"
    exit 127
fi

if [ ! -x "$AGENT_BINARY" ]; then
    echo "ERROR: Agent binary $AGENT_BINARY is not executable!"
    ls -l "$AGENT_BINARY"
    exit 126
fi

exec "$AGENT_BINARY" "$@" 