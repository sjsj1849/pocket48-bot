#!/bin/bash
# Build the bot
go build -o bot_48 cmd/bot/main.go

# Run the bot
if [ $? -eq 0 ]; then
    ./bot_48
else
    echo "Build failed"
    exit 1
fi
