#!/bin/bash

# Exit on error
set -e

# ANSI Color codes for formatting
GREEN='\033[0;32m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test Data UUIDs based on scripts/seed.sql
USER_ID="11111111-1111-1111-1111-111111111111"
SESSION_ID="aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

echo -e "${CYAN}======================================================${NC}"
echo -e "${CYAN}      CerberOS Memory Service CLI Demo Script         ${NC}"
echo -e "${CYAN}======================================================${NC}\n"

# Step 1: Ensure we are in the correct directory and build the binary
echo -e "${YELLOW}[1/3] Checking for memory-cli binary...${NC}"
cd "$(dirname "$0")/../../"

if [ ! -f "memory-cli" ]; then
    echo -e "Building memory-cli..."
    go build -o memory-cli ./cmd/cli
    echo -e "${GREEN}✓ Build successful.${NC}\n"
else
    echo -e "${GREEN}✓ Binary exists.${NC}\n"
fi

# Step 2: Set up Environment Variables for Direct DB Connection
echo -e "${YELLOW}[2/3] Setting up DB Environment Variables for Zero-Latency mode...${NC}"
export DB_USER="user"
export DB_PASSWORD="password"
export DB_NAME="memory_db"
export DB_HOST="localhost"
export DB_PORT="5432"
echo -e "${GREEN}✓ Environment variables set.${NC}\n"

# Step 3: Run the Demo Commands
echo -e "${YELLOW}[3/3] Running CLI Commands...${NC}\n"

# --- Demo 1: Facts Query ---
echo -e "${BLUE}▶ COMMAND 1: Semantic Search on User Facts${NC}"
echo -e "${CYAN}$ ./memory-cli -db \"env\" facts query --user $USER_ID \"what programming language do I prefer?\"${NC}"
sleep 1
./memory-cli -db "env" facts query --user "$USER_ID" "what programming language do I prefer?"
echo -e "\n"
sleep 2

# --- Demo 2: Save a New Fact ---
echo -e "${BLUE}▶ COMMAND 2: Save a New Fact to the Database${NC}"
echo -e "${CYAN}$ ./memory-cli -db \"env\" facts save --user $USER_ID \"I am presenting a CLI demo in class today.\"${NC}"
sleep 1
./memory-cli -db "env" facts save --user "$USER_ID" "I am presenting a CLI demo in class today."
echo -e "\n"
sleep 2

# --- Demo 3: Retrieve All Facts ---
echo -e "${BLUE}▶ COMMAND 3: Retrieve All Facts for User${NC}"
echo -e "${CYAN}$ ./memory-cli -db \"env\" facts all --user $USER_ID${NC}"
sleep 1
./memory-cli -db "env" facts all --user "$USER_ID"
echo -e "\n"
sleep 2

# --- Demo 4: Chat History ---
echo -e "${BLUE}▶ COMMAND 4: Retrieve Chat History for a Session${NC}"
echo -e "${CYAN}$ ./memory-cli -db \"env\" chat history --session $SESSION_ID --limit 3${NC}"
sleep 1
./memory-cli -db "env" chat history --session "$SESSION_ID" --limit 3
echo -e "\n"
sleep 2

# --- Demo 5: System Events ---
echo -e "${BLUE}▶ COMMAND 5: Retrieve System Events Logs${NC}"
echo -e "${CYAN}$ ./memory-cli -db \"env\" system events --limit 2${NC}"
sleep 1
./memory-cli -db "env" system events --limit 2
echo -e "\n"
sleep 2

echo -e "${GREEN}======================================================${NC}"
echo -e "${GREEN}                  Demo Complete!                      ${NC}"
echo -e "${GREEN}======================================================${NC}"
