while [ ! -d /nfs/vmware/vcf/nfs-mount/vsan-hcl ]; do
    echo "[INFO] Waiting for vsan-hcl folder..."
    sleep 5
done
while [ ! -f /nfs/vmware/vcf/nfs-mount/vsan-hcl/all.json ]; do
    echo "[INFO] Waiting for all.json..."
    sleep 5
done
echo "[INFO] Overwriting all.json..."
mv /home/vcf/all.json /nfs/vmware/vcf/nfs-mount/vsan-hcl/all.json
chmod 777 /nfs/vmware/vcf/nfs-mount/vsan-hcl/all.json
chown vcf_lcm:vcf /nfs/vmware/vcf/nfs-mount/vsan-hcl/all.json
echo "[INFO] Script Execution completed"
