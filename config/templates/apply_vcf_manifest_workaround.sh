#!/bin/bash

# This script implements the workaround for the "NSX install image validation failed" error in VCF 9.0.0.0

echo "Starting VCF manifest workaround for deploying VCF 9.0.0.0 using VCF 9.0.1.0 Installer..."

cd /home/vcf

echo "Backing up vcfManifest.json..."
cp /nfs/vmware/vcf/nfs-mount/metadata/vcfManifest.json /home/vcf/vcfManifestWithout901Holodeck.json
cp /nfs/vmware/vcf/nfs-mount/metadata/vcfManifest.json /home/vcf/vcfManifestWith901Holodeck.json

# Step 3: Remove the 9.0.1.0 block from the manifest and fix JSON syntax
echo "Removing VCF 9.0.1.0 release block from vcfManifestWithout901Holodeck.json using jq..."
jq 'del(.releases[] | select(.version == "9.0.1.0"))' vcfManifestWithout901Holodeck.json > temp_manifest.json && mv temp_manifest.json vcfManifestWithout901Holodeck.json

# Verify the JSON syntax
echo "Verifying JSON syntax of modified manifest..."
jq . vcfManifestWithout901Holodeck.json | head -n 3

echo "Clearing manifest from local database..."
psql -h localhost -U postgres lcm -c "DELETE FROM manifest;"

# Step 6: Get a token to authenticate with the API (as root)
echo "Getting authentication token for admin@local..."
VCF_ADMIN_PASSWORD=$1 # Read password from the first argument
if [ -z "$VCF_ADMIN_PASSWORD" ]; then
    echo "Error: VCF Admin password not provided. Exiting."
    exit 1
fi

TOKEN=$(curl -H 'Content-Type:application/json' https://localhost/v1/tokens -d "{\"username\":\"admin@local\",\"password\":\"$VCF_ADMIN_PASSWORD\"}" -k | jq -r '.accessToken')

if [ -z "$TOKEN" ]; then
    echo "Failed to get authentication token. Please check the password and try again."
    exit 1
fi

echo "Token obtained successfully."

echo "Uploading the modified manifest..."
curl -k -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" -X POST https://localhost/v1/manifests -d "@/home/vcf/vcfManifestWithout901Holodeck.json"

echo "Workaround script finished."
