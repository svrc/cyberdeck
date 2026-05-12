#!/bin/bash

# --- Argument Validation ---
# Check if exactly two arguments (password and file path) are provided.
if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <password> <path_to_manifest_file>"
    exit 1
fi

PASSWORD="$1"
MANIFEST_PATH="$2"

# Check if the manifest file exists and is a regular file.
if [ ! -f "$MANIFEST_PATH" ]; then
    echo "Error: Manifest file not found at '$MANIFEST_PATH'"
    exit 1
fi

# --- Step 1: Clear the manifest table in PostgreSQL ---
echo "Step 1: Deleting old manifest data..."
psql -h localhost -U postgres lcm -c "DELETE FROM manifest;"
echo "Step 1: Complete."

# --- Step 2: Get Authentication Token ---
echo "Step 2: Authenticating with user admin@local to get access token..."
# The -s flag silences curl's progress meter.
# The JSON data is enclosed in double quotes to allow for the $PASSWORD variable.
# Inner double quotes within the JSON are escaped with backslashes (\").
TOKEN=$(curl -s -k \
    -H 'Content-Type: application/json' \
    -X POST "https://localhost/v1/tokens" \
    -d "{\"username\": \"admin@local\", \"password\": \"${PASSWORD}\"}" | jq -r '.accessToken')

# Validate that the token was successfully retrieved.
if [ -z "$TOKEN" ] || [ "$TOKEN" == "null" ]; then
    echo "Error: Failed to retrieve access token. Check credentials or service availability."
    exit 1
fi
echo "Step 2: Token retrieved successfully."

# --- Step 3: Upload the new manifest file ---
echo "Step 3: Uploading new manifest from '$MANIFEST_PATH'..."
# The @ symbol before the path tells curl to read POST data from that file.
curl -s -k \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKEN" \
    -X POST "https://localhost/v1/manifests" \
    -d "@$MANIFEST_PATH"
echo "Step 3: Manifest upload command executed."
