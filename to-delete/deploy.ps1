# Deploy script for 3x-ui to production server
# Run from PowerShell: .\deploy.ps1

$SRC = "\\wsl$\Ubuntu\home\modulator\Development\3x-ui-2.8.11\x-ui"

Write-Host "Uploading x-ui to prod-server..." -ForegroundColor Cyan
scp $SRC prod-server:/tmp/x-ui
if ($LASTEXITCODE -ne 0) { Write-Host "Upload failed!" -ForegroundColor Red; exit 1 }

Write-Host "Installing and restarting..." -ForegroundColor Cyan
ssh prod-server "mv /tmp/x-ui /usr/local/x-ui/x-ui && chmod +x /usr/local/x-ui/x-ui && systemctl restart x-ui && echo 'Done'"

Write-Host "Deployed!" -ForegroundColor Green
